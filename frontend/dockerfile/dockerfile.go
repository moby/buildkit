package dockerfile

import (
	"io/ioutil"
	"path"
	"path/filepath"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/snapshot"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

const (
	keyTarget   = "target"
	keyFilename = "filename"
)

type dfFrontend struct{}

func NewDockerfileFrontend() frontend.Frontend {
	return &dfFrontend{}
}

func (f *dfFrontend) Solve(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string) (retRef cache.ImmutableRef, retErr error) {

	filename := opts[keyFilename]
	if filename == "" {
		filename = "Dockerfile"
	}
	if path.Base(filename) != filename {
		return nil, errors.Errorf("invalid filename %s", filename)
	}

	src := llb.Local("dockerfile", llb.IncludePatterns([]string{filename}))
	dt, err := src.Marshal()
	if err != nil {
		return nil, err
	}

	ref, err := llbBridge.Solve(ctx, dt)
	if err != nil {
		return nil, err
	}

	defer func() {
		if ref != nil {
			ref.Release(context.TODO())
		}
	}()

	mount, err := ref.Mount(ctx, false)
	if err != nil {
		return nil, err
	}

	lm := snapshot.LocalMounter(mount)

	root, err := lm.Mount()
	if err != nil {
		return nil, err
	}

	defer func() {
		if lm != nil {
			lm.Unmount()
		}
	}()

	dtDockerfile, err := ioutil.ReadFile(filepath.Join(root, filename))
	if err != nil {
		return nil, err
	}

	if err := lm.Unmount(); err != nil {
		return nil, err
	}
	lm = nil

	if err := ref.Release(ctx); err != nil {
		return nil, err
	}
	ref = nil

	st, err := dockerfile2llb.Dockerfile2LLB(ctx, dtDockerfile, dockerfile2llb.ConvertOpt{
		Target:       opts[keyTarget],
		MetaResolver: llb.DefaultImageMetaResolver(),
	})
	if err != nil {
		return nil, err
	}

	dt, err = st.Marshal()
	if err != nil {
		return nil, err
	}

	retRef, err = llbBridge.Solve(ctx, dt)
	if err != nil {
		return nil, err
	}

	return retRef, nil
}
