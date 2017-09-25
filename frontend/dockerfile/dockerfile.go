package dockerfile

import (
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/snapshot"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

const (
	keyTarget             = "target"
	keyFilename           = "filename"
	exporterImageConfig   = "containerimage.config"
	defaultDockerfileName = "Dockerfile"
	localNameDockerfile   = "dockerfile"
	buildArgPrefix        = "build-arg:"
)

func NewDockerfileFrontend() frontend.Frontend {
	return &dfFrontend{}
}

type dfFrontend struct{}

func (f *dfFrontend) Solve(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string) (retRef cache.ImmutableRef, exporterAttr map[string]interface{}, retErr error) {

	filename := opts[keyFilename]
	if filename == "" {
		filename = defaultDockerfileName
	}
	if path.Base(filename) != filename {
		return nil, nil, errors.Errorf("invalid filename %s", filename)
	}

	src := llb.Local(localNameDockerfile, llb.IncludePatterns([]string{filename}))
	dt, err := src.Marshal()
	if err != nil {
		return nil, nil, err
	}

	ref, err := llbBridge.Solve(ctx, dt)
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		if ref != nil {
			ref.Release(context.TODO())
		}
	}()

	mount, err := ref.Mount(ctx, false)
	if err != nil {
		return nil, nil, err
	}

	lm := snapshot.LocalMounter(mount)

	root, err := lm.Mount()
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		if lm != nil {
			lm.Unmount()
		}
	}()

	dtDockerfile, err := ioutil.ReadFile(filepath.Join(root, filename))
	if err != nil {
		return nil, nil, err
	}

	if err := lm.Unmount(); err != nil {
		return nil, nil, err
	}
	lm = nil

	if err := ref.Release(context.TODO()); err != nil {
		return nil, nil, err
	}
	ref = nil

	st, img, err := dockerfile2llb.Dockerfile2LLB(ctx, dtDockerfile, dockerfile2llb.ConvertOpt{
		Target:       opts[keyTarget],
		MetaResolver: llbBridge,
		BuildArgs:    filterBuildArgs(opts),
	})

	if err != nil {
		return nil, nil, err
	}

	dt, err = st.Marshal()
	if err != nil {
		return nil, nil, err
	}

	retRef, err = llbBridge.Solve(ctx, dt)
	if err != nil {
		return nil, nil, err
	}

	return retRef, map[string]interface{}{
		exporterImageConfig: img,
	}, nil
}

func filterBuildArgs(opt map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range opt {
		if strings.HasPrefix(k, buildArgPrefix) {
			m[strings.TrimPrefix(k, buildArgPrefix)] = v
		}
	}
	return m
}
