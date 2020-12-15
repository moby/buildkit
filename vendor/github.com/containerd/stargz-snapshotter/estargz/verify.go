/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package estargz

import (
	"compress/gzip"
	"fmt"
	"io"
	"sync"

	"github.com/containerd/stargz-snapshotter/estargz/stargz"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// newVerifiableStargz takes stargz archive and returns modified one which contains
// contents digests in the TOC JSON. This also returns the digest of TOC JSON
// itself which can be used for verifying the TOC JSON.
func newVerifiableStargz(sgz *io.SectionReader) (io.Reader, digest.Digest, error) {
	// Extract TOC JSON and some related parts
	blob, err := parseStargz(sgz)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to extract TOC JSON")
	}

	// Rewrite the original TOC JSON with chunks digests
	if err := addDigests(blob.jtoc, sgz); err != nil {
		return nil, "", errors.Wrap(err, "failed to digest stargz")
	}
	tocJSON, tocDigest, err := marshalTOCJSON(blob.jtoc)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to marshal TOC JSON")
	}

	// Reconstruct stargz file with the modified TOC JSON
	if _, err := blob.payload.Seek(0, io.SeekStart); err != nil {
		return nil, "", errors.Wrap(err, "failed to reset the seek position of stargz")
	}
	if _, err := blob.footer.Seek(0, io.SeekStart); err != nil {
		return nil, "", errors.Wrap(err, "failed to reset the seek position of footer")
	}
	return io.MultiReader(
		blob.payload, // Original stargz (before TOC JSON)
		tocJSON,      // Rewritten TOC JSON
		blob.footer,  // Unmodified footer (because tocOffset is unchanged)
	), tocDigest, nil
}

// VerifyStargzTOC checks that the TOC JSON in the passed stargz blob matches the
// passed digests and that the TOC JSON contains digests for all chunks
// contained in the stargz blob. If the verification succceeds, this function
// returns TOCEntryVerifier which holds all chunk digests in the stargz blob.
func VerifyStargzTOC(sgz *io.SectionReader, tocDigest digest.Digest) (TOCEntryVerifier, error) {
	blob, err := parseStargz(sgz)
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract TOC JSON")
	}

	// Verify the digest of TOC JSON
	if blob.jtocDigest != tocDigest {
		return nil, fmt.Errorf("invalid TOC JSON %q; want %q", blob.jtocDigest, tocDigest)
	}

	// Collect all chunks digests in this layer
	digestMap := make(map[int64]digest.Digest) // map from chunk offset to the digest
	for _, e := range blob.jtoc.Entries {
		if e.Type == "reg" || e.Type == "chunk" {
			if e.Type == "reg" && e.Size == 0 {
				continue // ignores empty file
			}

			// offset must be unique in stargz blob
			if _, ok := digestMap[e.Offset]; ok {
				return nil, fmt.Errorf("offset %d found twice", e.Offset)
			}

			// all chunk entries must contain digest
			if e.ChunkDigest == "" {
				return nil, fmt.Errorf("ChunkDigest of %q(off=%d) not found in TOC JSON",
					e.Name, e.Offset)
			}

			d, err := digest.Parse(e.ChunkDigest)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse digest %q", e.ChunkDigest)
			}
			digestMap[e.Offset] = d
		}
	}

	return &verifier{digestMap: digestMap}, nil
}

// TOCEntryVerifier holds verifiers that are usable for verifying chunks contained
// in a stargz blob.
type TOCEntryVerifier interface {

	// Verifier provides a content verifier that can be used for verifying the
	// contents of the specified TOCEntry.
	Verifier(ce *stargz.TOCEntry) (digest.Verifier, error)
}

// verifier is an implementation of TOCEntryVerifier which holds verifiers keyed by
// offset of the chunk.
type verifier struct {
	digestMap   map[int64]digest.Digest
	digestMapMu sync.Mutex
}

// Verifier returns a content verifier specified by TOCEntry.
func (v *verifier) Verifier(ce *stargz.TOCEntry) (digest.Verifier, error) {
	v.digestMapMu.Lock()
	defer v.digestMapMu.Unlock()
	d, ok := v.digestMap[ce.Offset]
	if !ok {
		return nil, fmt.Errorf("verifier for offset=%d,size=%d hasn't been registered",
			ce.Offset, ce.ChunkSize)
	}
	return d.Verifier(), nil
}

// addDigests calculates chunk digests and adds them to the TOC JSON
func addDigests(toc *jtoc, sgz *io.SectionReader) error {
	var (
		sizeMap = make(map[string]int64)
		zr      *gzip.Reader
		err     error
	)
	for _, e := range toc.Entries {
		if e.Type == "reg" || e.Type == "chunk" {
			if e.Type == "reg" {
				sizeMap[e.Name] = e.Size // saves the file size for futural use
			}
			size := e.ChunkSize
			if size == 0 {
				// In the seriarized form, ChunkSize field of the last chunk in a file
				// is zero so we need to calculate it.
				size = sizeMap[e.Name] - e.ChunkOffset
			}
			if size > 0 {
				if _, err := sgz.Seek(e.Offset, io.SeekStart); err != nil {
					return errors.Wrapf(err, "failed to seek to %q", e.Name)
				}
				if zr == nil {
					if zr, err = gzip.NewReader(sgz); err != nil {
						return errors.Wrapf(err, "failed to decomp %q", e.Name)
					}
				} else {
					if err := zr.Reset(sgz); err != nil {
						return errors.Wrapf(err, "failed to decomp %q", e.Name)
					}
				}
				dgstr := digest.Canonical.Digester()
				if _, err := io.CopyN(dgstr.Hash(), zr, size); err != nil {
					return errors.Wrapf(err, "failed to read %q; (file size: %d, e.Offset: %d, e.ChunkOffset: %d, chunk size: %d, end offset: %d, size of stargz file: %d)",
						e.Name, sizeMap[e.Name], e.Offset, e.ChunkOffset, size,
						e.Offset+size, sgz.Size())
				}
				e.ChunkDigest = dgstr.Digest().String()
			}
		}
	}

	return nil
}
