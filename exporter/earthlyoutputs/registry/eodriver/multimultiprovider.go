package eodriver

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/moby/buildkit/util/contentutil"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// MultiMultiProviderSingleton is a multi-multi-provider that can be shared across runs.
// The reason this is a singleton is that it can be used as a basis for a registry storage driver.
var MultiMultiProviderSingleton = NewMultiMultiProvider()

// NewMultiMultiProvider creates a new multi multi provider.
func NewMultiMultiProvider() *MultiMultiProvider {
	return &MultiMultiProvider{
		imgs:    make(map[string]*imgData),
		digests: make(map[digest.Digest]map[string]bool),
	}
}

type imgData struct {
	base     content.Provider
	baseDgst digest.Digest
	subs     map[digest.Digest]content.Provider
}

// MultiMultiProvider is a provider backed by a set of images, each of which is made out of
// a set of providers.
type MultiMultiProvider struct {
	mu      sync.RWMutex
	imgs    map[string]*imgData               // imgName -> imgData
	digests map[digest.Digest]map[string]bool // digest -> set of imgName
}

func (mmp *MultiMultiProvider) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error) {
	mmp.mu.RLock()
	defer mmp.mu.RUnlock()
	imgs := mmp.digests[desc.Digest]
	// take the first one if multiple are found.
	for imgName := range imgs {
		mp, _, err := mmp.getNoLock(ctx, imgName)
		if err != nil {
			continue
		}
		return mp.ReaderAt(ctx, desc)
	}
	return nil, errors.Wrapf(errdefs.ErrNotFound, "content %v", desc.Digest)
}

// Get returns a read-only MultiProvider and the base digest for a given imgName.
func (mmp *MultiMultiProvider) Get(ctx context.Context, imgName string) (*contentutil.MultiProvider, digest.Digest, error) {
	mmp.mu.RLock()
	defer mmp.mu.RUnlock()
	return mmp.getNoLock(ctx, imgName)
}

func (mmp *MultiMultiProvider) getNoLock(ctx context.Context, imgName string) (*contentutil.MultiProvider, digest.Digest, error) {
	imgData, ok := mmp.imgs[imgName]
	if !ok {
		return nil, "", errors.Wrapf(errdefs.ErrNotFound, "img name %v", imgName)
	}
	mp := contentutil.NewMultiProvider(imgData.base)
	for dgst, p := range imgData.subs {
		mp.Add(dgst, p)
	}
	return mp, imgData.baseDgst, nil
}

// AddImgSub adds a new child content provider for an image.
func (mmp *MultiMultiProvider) AddImgSub(imgName string, dgst digest.Digest, p content.Provider) error {
	mmp.mu.Lock()
	defer mmp.mu.Unlock()
	imgData, ok := mmp.imgs[imgName]
	if !ok {
		return errors.Wrapf(errdefs.ErrNotFound, "img name %v", imgName)
	}
	imgData.subs[dgst] = p
	mmp.addDigestEntry(dgst, imgName)
	return nil
}

// AddImg adds a new child image. The image is removed from the collection when the context is canceled.
func (mmp *MultiMultiProvider) AddImg(ctx context.Context, imgName string, base content.Provider, baseDigest digest.Digest) error {
	// The config digest needs to be mapped manually - read out the manifest
	// and find the config digest in there. Do most of this outside of the lock.
	mfstRa, err := base.ReaderAt(ctx, ocispec.Descriptor{Digest: baseDigest})
	if err != nil {
		return err
	}
	mfstDt := make([]byte, mfstRa.Size())
	_, err = mfstRa.ReadAt(mfstDt, 0)
	if err != nil {
		return err
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	err = json.Unmarshal(mfstDt, &manifest)
	if err != nil {
		return err
	}
	configDgst := digest.Digest(manifest.Config.Digest)

	mmp.mu.Lock()
	defer mmp.mu.Unlock()
	mmp.maybeDelete(imgName)
	imgData := &imgData{
		base:     base,
		baseDgst: baseDigest,
		subs:     make(map[digest.Digest]content.Provider),
	}
	mmp.imgs[imgName] = imgData
	imgData.subs[baseDigest] = base
	mmp.addDigestEntry(baseDigest, imgName)
	mmp.addDigestEntry(configDgst, imgName)

	go func() {
		<-ctx.Done()
		mmp.mu.Lock()
		defer mmp.mu.Unlock()
		mmp.maybeDelete(imgName)
	}()
	return nil
}

func (mmp *MultiMultiProvider) addDigestEntry(dgst digest.Digest, imgName string) {
	imgSet, ok := mmp.digests[dgst]
	if !ok {
		imgSet = make(map[string]bool)
		mmp.digests[dgst] = imgSet
	}
	imgSet[imgName] = true
}

func (mmp *MultiMultiProvider) maybeDelete(imgName string) {
	imgData, ok := mmp.imgs[imgName]
	if !ok {
		return
	}
	for dgst := range imgData.subs {
		delete(mmp.digests[dgst], imgName)
		if len(mmp.digests[dgst]) == 0 {
			delete(mmp.digests, dgst)
		}
	}
	delete(mmp.imgs, imgName)
}
