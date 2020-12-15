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
	"io"

	"github.com/containerd/stargz-snapshotter/estargz/stargz"
	digest "github.com/opencontainers/go-digest"
)

const (
	// TOCJSONDigestAnnotation is an annotation for image manifest. This stores the
	// digest of the TOC JSON
	TOCJSONDigestAnnotation = "containerd.io/snapshot/stargz/toc.digest"

	// PrefetchLandmark is a file entry which indicates the end position of
	// prefetch in the stargz file.
	PrefetchLandmark = ".prefetch.landmark"

	// NoPrefetchLandmark is a file entry which indicates that no prefetch should
	// occur in the stargz file.
	NoPrefetchLandmark = ".no.prefetch.landmark"
)

// jtoc is a TOC JSON schema which contains additional fields for chunk-level
// content verification.
type jtoc struct {

	// Version is a field to store the version information of TOC JSON.
	Version int `json:"version"`

	// Entries is a field to store TOCEntries in this archive. These TOCEntries
	// are extended in this package for content verification.
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

// stargzBlob is a structured view of a eStargz blob
type stargzBlob struct {
	// jtoc is a decoded TOC JSON struct
	jtoc *jtoc

	// jtocDigest is OCI digest of TOC JSON. formed as "sha256:abcd1234..."
	jtocDigest digest.Digest

	// jtocOffset is the offset of TOC JSON in the stargz blob.
	jtocOffset int64

	// payload is a reader of the blob but doesn't contain TOC JSON and footer.
	payload io.ReadSeeker

	// footer is the footer defined by stargz, which contains the offset to TOC JSON.
	footer io.ReadSeeker
}
