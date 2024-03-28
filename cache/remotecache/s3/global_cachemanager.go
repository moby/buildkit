package s3

import (
	"strconv"
	"strings"
	"time"

	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/contentutil"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	links     = "links/"
	backlinks = "backlinks/"
	manifests = "manifests/"
	separator = "::"
)

type GlobalCacheRecord struct {
	// ID is Digest("[[parent.ID?,...],...]::record.Digest")
	ID digest.Digest `json:"id,omitempty"`
	// The record digest, as given by Buildkit.
	Digest digest.Digest `json:"digest,omitempty"`
}

type GlobalCacheLayer struct {
	Blob        digest.Digest        `json:"blob,omitempty"`
	ParentIndex int                  `json:"parent,omitempty"`
	Annotations *v1.LayerAnnotations `json:"annotations,omitempty"`
}

func vertexPrefix[S1 ~string, S2 ~string, S3 ~string](parentID S1, recordDigest S2, inputIdx int, selector S3) string {
	prefix := strings.Join([]string{string(parentID), string(recordDigest), strconv.Itoa(inputIdx), string(selector)}, separator)
	return prefix
}

// MiniManifest
type MiniManifest struct {
	// Layers is an indexed list of layers referenced by the manifest.
	Layers []v1.CacheLayer `json:"layers"`

	// Creation time of the layers, not actually stored in the file, but stored
	// in S3 metadata.
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// unique ID per remote. this ID is not stable.
func remoteID(r *solver.Remote) string {
	dgstr := digest.Canonical.Digester()
	for _, desc := range r.Descriptors {
		dgstr.Hash().Write([]byte(desc.Digest))
	}
	return dgstr.Digest().String()
}

func (mm *MiniManifest) getRemoteChain(provider v1.DescriptorProvider) (*solver.Remote, error) {
	visited := map[int]struct{}{}
	return mm._getRemoteChain(len(mm.Layers)-1, provider, visited)
}

func (mm *MiniManifest) _getRemoteChain(idx int, provider v1.DescriptorProvider, visited map[int]struct{}) (*solver.Remote, error) {
	if _, ok := visited[idx]; ok {
		return nil, errors.Errorf("invalid looping layer")
	}
	visited[idx] = struct{}{}

	if idx < 0 || idx >= len(mm.Layers) {
		return nil, errors.Errorf("invalid layer index %d", idx)
	}

	l := mm.Layers[idx]

	descPair, ok := provider[l.Blob]
	if !ok {
		return nil, nil
	}

	var r *solver.Remote
	if l.ParentIndex != -1 {
		var err error
		r, err = mm._getRemoteChain(l.ParentIndex, provider, visited)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, nil
		}
		r.Descriptors = append(r.Descriptors, descPair.Descriptor)
		mp := contentutil.NewMultiProvider(r.Provider)
		mp.Add(descPair.Descriptor.Digest, descPair.Provider)
		r.Provider = mp
		return r, nil
	}
	return &solver.Remote{
		Descriptors: []ocispecs.Descriptor{descPair.Descriptor},
		Provider:    descPair.Provider,
	}, nil
}
