package cache

import (
	"io/ioutil"
	"path/filepath"

	"github.com/docker/docker/pkg/symlink"
	"github.com/moby/buildkit/snapshot"
	"golang.org/x/net/context"
)

func ReadFile(ctx context.Context, ref ImmutableRef, p string) ([]byte, error) {
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

	fp, err := symlink.FollowSymlinkInScope(filepath.Join(root, p), root)
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
