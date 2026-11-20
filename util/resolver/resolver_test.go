package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/pkg/reference"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/util/pull"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestResolveModeNever(t *testing.T) {
	ctx := t.Context()
	desc := ocispecs.Descriptor{Digest: digest.FromString("local image")}
	for _, tc := range []struct {
		name    string
		store   images.Store
		wantErr bool
	}{
		{
			name:    "no image store",
			wantErr: true,
		},
		{
			name:    "missing image",
			store:   &neverImageStore{err: cerrdefs.ErrNotFound},
			wantErr: true,
		},
		{
			name:  "local image",
			store: &neverImageStore{target: desc},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil remote resolver makes any attempt to use the registry fail.
			r := (&Resolver{}).WithImageStore(tc.store, ResolveModeNever)
			name, got, err := r.Resolve(ctx, "example.com/image:latest")
			if tc.wantErr {
				require.ErrorIs(t, err, cerrdefs.ErrNotFound)
			} else {
				require.NoError(t, err)
				require.Equal(t, "example.com/image:latest", name)
				require.Equal(t, desc, got)
			}
			fetcher, err := r.Fetcher(ctx, "example.com/image:latest")
			require.NoError(t, err)
			_, err = fetcher.Fetch(ctx, desc)
			require.ErrorIs(t, err, cerrdefs.ErrNotFound)
			require.ErrorContains(t, err, "pulling is disabled")
		})
	}
	mode, err := ParseImageResolveMode(pb.AttrImageResolveModeNever)
	require.NoError(t, err)
	require.Equal(t, ResolveModeNever, mode)
	require.Equal(t, pb.AttrImageResolveModeNever, mode.String())
}

type neverImageStore struct {
	images.Store
	target ocispecs.Descriptor
	err    error
}

func (s *neverImageStore) Get(context.Context, string) (images.Image, error) {
	return images.Image{Target: s.target}, s.err
}

func TestNeverLocalContent(t *testing.T) {
	ctx := t.Context()
	store := contentutil.NewBuffer()
	write := func(v any, mediaType string) ocispecs.Descriptor {
		t.Helper()
		dt, err := json.Marshal(v)
		require.NoError(t, err)
		desc := ocispecs.Descriptor{
			MediaType: mediaType,
			Digest:    digest.FromBytes(dt),
			Size:      int64(len(dt)),
		}
		require.NoError(t, content.WriteBlob(ctx, store, desc.Digest.String(), bytes.NewReader(dt), desc))
		return desc
	}
	platform := platforms.MustParse("linux/amd64")
	config := write(ocispecs.Image{
		Platform: platform,
		RootFS: ocispecs.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{digest.FromString("missing layer")},
		},
	}, ocispecs.MediaTypeImageConfig)
	layer := ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageLayer,
		Digest:    digest.FromString("missing layer"),
		Size:      13,
	}
	manifest := write(ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Config:    config,
		Layers:    []ocispecs.Descriptor{layer},
	}, ocispecs.MediaTypeImageManifest)
	r := (&Resolver{}).WithImageStore(&neverImageStore{target: manifest}, ResolveModeNever)
	const ref = "example.com/image:latest"
	_, dt, err := imageutil.Config(ctx, ref, r, store, nil, &platform)
	require.NoError(t, err)
	require.NotEmpty(t, dt)
	// Cached digest references also work on workers without an image store.
	noStore := (&Resolver{}).WithImageStore(nil, ResolveModeNever)
	_, _, err = imageutil.Config(ctx, "example.com/image@"+manifest.Digest.String(), noStore, store, nil, &platform)
	require.NoError(t, err)
	src, err := reference.Parse(ref)
	require.NoError(t, err)
	p := &pull.Puller{
		ContentStore: store,
		Resolver:     r,
		Src:          src,
		Platform:     platform,
	}
	manifests, err := p.PullManifests(ctx, func(session.Group) remotes.Resolver { return r })
	require.NoError(t, err)
	_, err = manifests.Provider(nil).ReaderAt(ctx, layer)
	require.ErrorIs(t, err, cerrdefs.ErrNotFound)
	require.ErrorContains(t, err, "pulling is disabled")
	store = contentutil.NewBuffer()
	write(ocispecs.Manifest{
		MediaType: ocispecs.MediaTypeImageManifest,
		Config:    config,
		Layers:    []ocispecs.Descriptor{layer},
	}, ocispecs.MediaTypeImageManifest)
	_, _, err = imageutil.Config(ctx, ref, r, store, nil, &platform)
	require.ErrorIs(t, err, cerrdefs.ErrNotFound)
	require.ErrorContains(t, err, "pulling is disabled")
}

func TestNewMirrorRegistryHost(t *testing.T) {
	const testConfig = `
[registry."docker.io"]
mirrors = ["hub.docker.io", "yourmirror.local:5000/proxy.docker.io"]
[registry."quay.io"]
mirrors = ["yourmirror.local:5000/proxy.quay.io"]
[registry."fake.io"]
mirrors = ["https://url/", "https://url/path/"]
`

	tests := map[string]struct {
		description string
		host        string
		path        string
	}{
		"hub.docker.io": {
			description: "docker_io_mirror_without_path",
			host:        "hub.docker.io",
			path:        defaultPath,
		},
		"yourmirror.local:5000/proxy.docker.io": {
			description: "docker_io_mirror_with_path",
			host:        "yourmirror.local:5000",
			path:        path.Join(defaultPath, "proxy.docker.io"),
		},
		"yourmirror.local:5000/proxy.quay.io": {
			description: "docker_quay_mirror_with_path",
			host:        "yourmirror.local:5000",
			path:        path.Join(defaultPath, "proxy.quay.io"),
		},
		"https://url/": {
			description: "docker_fake_mirror_scheme_without_path",
			host:        "url",
			path:        defaultPath,
		},
		"https://url/path/": {
			description: "docker_fake_mirror_scheme_with_path",
			host:        "url",
			path:        path.Join(defaultPath, "path"),
		},
	}

	cfg, err := config.Load(bytes.NewBuffer([]byte(testConfig)))
	require.NoError(t, err)

	require.NotEqual(t, 0, len(cfg.Registries))
	for _, registry := range cfg.Registries {
		require.NotEqual(t, 0, len(registry.Mirrors))
		for _, m := range registry.Mirrors {
			test := tests[m]
			h := newMirrorRegistryHost(m)
			require.NotNil(t, h)
			require.Equal(t, h.Host, test.host)
			require.Equal(t, h.Path, test.path)
		}
	}
}
