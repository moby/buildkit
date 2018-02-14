package cache

import (
	"context"
	"io/ioutil"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/snapshot"
)

func ReadFile(ctx context.Context, ref ImmutableRef, p string) ([]byte, error) {
	mount, err := ref.Mount(ctx, true)
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

	fp, err := fs.RootPath(root, p)
	if err != nil {
		return nil, err
	}

	dt, err := ioutil.ReadFile(fp)
	if err != nil {
		return nil, err
	}
	if err := lm.Unmount(); err != nil {
		return nil, err
	}
	lm = nil
	return dt, err
}
