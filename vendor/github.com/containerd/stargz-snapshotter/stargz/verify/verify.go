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

package verify

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/google/crfs/stargz"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const (
	// TOCJSONDigestAnnotation is an annotation for image manifest. This stores the
	// digest of the TOC JSON
	TOCJSONDigestAnnotation = "containerd.io/snapshot/stargz/toc.digest"
)

// jtoc is a TOC JSON schema which contains additional fields for chunk-level
// content verification. This is still backward compatible to the official
// definition:
// https://github.com/google/crfs/blob/71d77da419c90be7b05d12e59945ac7a8c94a543/stargz/stargz.go
type jtoc struct {

	// Version is a field to store the version information of TOC JSON. This field
	// is unused in this package but is for the backwards-compatibility to stargz.
	Version int `json:"version"`

	// Entries is a field to store TOCEntries in this archive. These TOCEntries
	// are extended in this package for content verification, with keeping the
	// backward-compatibility with stargz.
	Entries []*TOCEntry `json:"entries"`
}

// TOCEntry extends stargz.TOCEntry with an additional field for storing chunks
// digests.
type TOCEntry struct {
	stargz.TOCEntry

	// ChunkDigest stores an OCI digest of the chunk. This must be formed
	// as "sha256:0123abcd...".
	ChunkDigest string `json:"chunkDigest,omitempty"`
}

// NewVerifiableStagz takes stargz archive and returns modified one which contains
// contents digests in the TOC JSON. This also returns the digest of TOC JSON
// itself which can be used for verifying the TOC JSON.
func NewVerifiableStagz(sgz *io.SectionReader) (newSgz io.Reader, jtocDigest digest.Digest, err error) {
	// Extract TOC JSON and some related parts
	toc, err := extractTOCJSON(sgz)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to extract TOC JSON")
	}

	// Rewrite the original TOC JSON with chunks digests
	if err := addDigests(toc.jtoc, sgz); err != nil {
		return nil, "", errors.Wrap(err, "failed to digest stargz")
	}
	tocJSON, err := json.Marshal(toc.jtoc)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to marshal TOC JSON")
	}
	dgstr := digest.Canonical.Digester()
	if _, err := io.CopyN(dgstr.Hash(), bytes.NewReader(tocJSON), int64(len(tocJSON))); err != nil {
		return nil, "", errors.Wrap(err, "failed to calculate digest of TOC JSON")
	}
	pr, pw := io.Pipe()
	go func() {
		zw, err := gzip.NewWriterLevel(pw, gzip.BestCompression)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		zw.Extra = []byte("stargz.toc") // this magic string might not be necessary but let's follow the official behaviour: https://github.com/google/crfs/blob/71d77da419c90be7b05d12e59945ac7a8c94a543/stargz/stargz.go#L596
		tw := tar.NewWriter(zw)
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     stargz.TOCTarName,
			Size:     int64(len(tocJSON)),
		}); err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := tw.Write(tocJSON); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := tw.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := zw.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	// Reconstruct stargz file with the modified TOC JSON
	if _, err := sgz.Seek(0, io.SeekStart); err != nil {
		return nil, "", errors.Wrap(err, "failed to reset the seek position of stargz")
	}
	if _, err := toc.footer.Seek(0, io.SeekStart); err != nil {
		return nil, "", errors.Wrap(err, "failed to reset the seek position of footer")
	}
	return io.MultiReader(
		io.LimitReader(sgz, toc.offset), // Original stargz (before TOC JSON)
		pr,                              // Rewritten TOC JSON
		toc.footer,                      // Unmodified footer (because tocOffset is unchanged)
	), dgstr.Digest(), nil
}

// StargzTOC checks that the TOC JSON in the passed stargz blob matches the
// passed digests and that the TOC JSON contains digests for all chunks
// contained in the stargz blob. If the verification succceeds, this function
// returns TOCEntryVerifier which holds all chunk digests in the stargz blob.
func StargzTOC(sgz *io.SectionReader, tocDigest digest.Digest) (TOCEntryVerifier, error) {
	toc, err := extractTOCJSON(sgz)
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract TOC JSON")
	}

	// Verify the digest of TOC JSON
	if toc.digest != tocDigest {
		return nil, fmt.Errorf("invalid TOC JSON %q; want %q", toc.digest, tocDigest)
	}

	// Collect all chunks digests in this layer
	digestMap := make(map[int64]digest.Digest) // map from chunk offset to the digest
	for _, e := range toc.jtoc.Entries {
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

// tocInfo is a set of information related to footer and TOC JSON in a stargz
type tocInfo struct {
	jtoc   *jtoc         // decoded TOC JSON
	digest digest.Digest // OCI digest of TOC JSON. formed as "sha256:abcd1234..."
	offset int64         // offset of TOC JSON in the stargz blob
	footer io.ReadSeeker // stargz footer
}

// extractTOCJSON extracts TOC JSON from the given stargz reader, with avoiding
// scanning the entire blob leveraging its footer. The parsed TOC JSON contains
// additional fields usable for chunk-level content verification.
func extractTOCJSON(sgz *io.SectionReader) (toc *tocInfo, err error) {
	// Parse stargz footer and get the offset of TOC JSON
	if sgz.Size() < stargz.FooterSize {
		return nil, errors.New("stargz data is too small")
	}
	footerReader := io.NewSectionReader(sgz, sgz.Size()-stargz.FooterSize, stargz.FooterSize)
	zr, err := gzip.NewReader(footerReader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to uncompress footer")
	}
	defer zr.Close()
	if len(zr.Header.Extra) != 22 {
		return nil, errors.Wrap(err, "invalid extra size; must be 22 bytes")
	} else if string(zr.Header.Extra[16:]) != "STARGZ" {
		return nil, errors.New("invalid footer; extra must contain magic string \"STARGZ\"")
	}
	tocOffset, err := strconv.ParseInt(string(zr.Header.Extra[:16]), 16, 64)
	if err != nil {
		return nil, errors.Wrap(err, "invalid footer; failed to get the offset of TOC JSON")
	} else if tocOffset > sgz.Size() {
		return nil, fmt.Errorf("invalid footer; offset of TOC JSON is too large (%d > %d)",
			tocOffset, sgz.Size())
	}

	// Decode the TOC JSON
	tocReader := io.NewSectionReader(sgz, tocOffset, sgz.Size()-tocOffset-stargz.FooterSize)
	if err := zr.Reset(tocReader); err != nil {
		return nil, errors.Wrap(err, "failed to uncompress TOC JSON targz entry")
	}
	tr := tar.NewReader(zr)
	h, err := tr.Next()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get TOC JSON tar entry")
	} else if h.Name != stargz.TOCTarName {
		return nil, fmt.Errorf("invalid TOC JSON tar entry name %q; must be %q",
			h.Name, stargz.TOCTarName)
	}
	dgstr := digest.Canonical.Digester()
	decodedJTOC := new(jtoc)
	if err := json.NewDecoder(io.TeeReader(tr, dgstr.Hash())).Decode(&decodedJTOC); err != nil {
		return nil, errors.Wrap(err, "failed to decode TOC JSON")
	}
	if _, err := tr.Next(); err != io.EOF {
		// We only accept stargz file that its TOC JSON resides at the end of that
		// file to avoid changing the offsets of the following file entries by
		// rewriting TOC JSON (The official stargz lib also puts TOC JSON at the end
		// of the stargz file at this mement).
		// TODO: in the future, we should relax this restriction.
		return nil, errors.New("TOC JSON must reside at the end of targz")
	}

	return &tocInfo{
		jtoc:   decodedJTOC,
		digest: dgstr.Digest(),
		offset: tocOffset,
		footer: footerReader,
	}, nil
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
