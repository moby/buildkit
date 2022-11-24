package containerimage

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"

	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter/attestation"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/version"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/spdx/tools-golang/jsonloader"
	"github.com/spdx/tools-golang/jsonsaver"
	"github.com/spdx/tools-golang/spdx"
)

var intotoPlatform ocispecs.Platform = ocispecs.Platform{
	Architecture: "unknown",
	OS:           "unknown",
}

// supplementSBOM modifies SPDX attestations to include the file layers
func supplementSBOM(ctx context.Context, s session.Group, target cache.ImmutableRef, targetRemote *solver.Remote, refs map[string]cache.ImmutableRef, att result.Attestation) (result.Attestation, error) {
	if att.Kind != gatewaypb.AttestationKindInToto {
		return att, nil
	}
	if att.InToto.PredicateType != intoto.PredicateSPDX {
		return att, nil
	}

	content, err := attestation.ReadAll(ctx, s, refs, att)
	if err != nil {
		return att, err
	}

	doc, err := decodeSPDX(content)
	if err != nil {
		return att, err
	}

	layers, err := newFileLayerFinder(target, targetRemote)
	if err != nil {
		return att, err
	}
	modifyFile := func(f *spdx.File2_2) error {
		if f.FileComment != "" {
			// Skip over files that already have a comment - since the data is
			// unstructured, we can't correctly overwrite this field without
			// possibly breaking some scanner functionality.
			return nil
		}

		_, desc, err := layers.find(ctx, s, f.FileName)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return nil
		}
		f.FileComment = fmt.Sprintf("layerID: %s", desc.Digest.String())
		return nil
	}
	for _, f := range doc.UnpackagedFiles {
		if err := modifyFile(f); err != nil {
			return att, err
		}
	}
	for _, p := range doc.Packages {
		for _, f := range p.Files {
			if err := modifyFile(f); err != nil {
				return att, err
			}
		}
	}

	doc.CreationInfo.CreatorTools = append(doc.CreationInfo.CreatorTools, "buildkit-"+version.Version)

	content, err = encodeSPDX(doc)
	if err != nil {
		return att, err
	}

	return result.Attestation{
		Kind:        att.Kind,
		Path:        att.Path,
		ContentFunc: func() ([]byte, error) { return content, nil },
		InToto:      att.InToto,
	}, nil
}

func decodeSPDX(dt []byte) (s *spdx.Document2_2, err error) {
	defer func() {
		// The spdx tools JSON loader is reported to be panicing sometimes
		if v := recover(); v != nil {
			s = nil
			err = errors.Errorf("an error occurred during SPDX JSON document parsing: %+v", v)
		}
	}()
	doc, err := jsonloader.Load2_2(bytes.NewReader(dt))
	if err != nil {
		return nil, errors.Wrap(err, "unable to decode spdx")
	}
	return doc, nil
}

func encodeSPDX(s *spdx.Document2_2) (dt []byte, err error) {
	defer func() {
		// The spdx tools JSON saver is reported to be panicing sometimes
		if v := recover(); v != nil {
			dt = nil
			err = errors.Errorf("an error occurred during SPDX JSON document parsing: %+v", v)
		}
	}()
	w := bytes.NewBuffer(nil)
	err = jsonsaver.Save2_2(s, w)
	if err != nil {
		return nil, errors.Wrap(err, "unable to encode spdx")
	}
	return w.Bytes(), nil
}

// fileLayerFinder finds the layer that contains a file, with caching to avoid
// repeated FileList lookups.
type fileLayerFinder struct {
	pending []fileLayerEntry
	cache   map[string]fileLayerEntry
}

type fileLayerEntry struct {
	ref  cache.ImmutableRef
	desc ocispecs.Descriptor
}

func newFileLayerFinder(target cache.ImmutableRef, remote *solver.Remote) (fileLayerFinder, error) {
	chain := target.LayerChain()
	descs := remote.Descriptors
	if len(chain) != len(descs) {
		return fileLayerFinder{}, errors.New("layer chain and descriptor list are not the same length")
	}

	pending := make([]fileLayerEntry, len(chain))
	for i, ref := range chain {
		pending[i] = fileLayerEntry{ref: ref, desc: descs[i]}
	}
	return fileLayerFinder{
		pending: pending,
		cache:   map[string]fileLayerEntry{},
	}, nil
}

// find finds the layer that contains the file, returning the ImmutableRef and
// descriptor for the layer.
//
// find is not concurrency-safe.
func (c *fileLayerFinder) find(ctx context.Context, s session.Group, filename string) (cache.ImmutableRef, *ocispecs.Descriptor, error) {
	// return immediately if we've already found the layer containing filename
	if cache, ok := c.cache[filename]; ok {
		return cache.ref, &cache.desc, nil
	}

	for len(c.pending) > 0 {
		// pop the last entry off the pending list (we traverse the layers backwards)
		pending := c.pending[len(c.pending)-1]
		files, err := pending.ref.FileList(ctx, s)
		if err != nil {
			return nil, nil, err
		}
		c.pending = c.pending[:len(c.pending)-1]

		found := false
		for _, f := range files {
			// add all files in this layer to the cache
			if _, ok := c.cache[f]; ok {
				continue
			}
			c.cache[f] = pending

			// if we found the file, return the layer (but finish populating the cache first)
			if f == filename {
				found = true
			}
		}
		if found {
			return pending.ref, &pending.desc, nil
		}
	}
	return nil, nil, fs.ErrNotExist
}
