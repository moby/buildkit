package cache

import (
	"context"
	"io"
	"io/ioutil"
	"os"

	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/snapshot"
)

type ReadRequest struct {
	Filename string
	Range    *FileRange
}

type FileRange struct {
	Offset int
	Length int
}

func withMount(ctx context.Context, ref ImmutableRef, cb func(string) error) error {
	mount, err := ref.Mount(ctx, true)
	if err != nil {
		return err
	}

	lm := snapshot.LocalMounter(mount)

	root, err := lm.Mount()
	if err != nil {
		return err
	}

	defer func() {
		if lm != nil {
			lm.Unmount()
		}
	}()

	if err := cb(root); err != nil {
		return err
	}

	if err := lm.Unmount(); err != nil {
		return err
	}
	lm = nil
	return nil
}

func ReadFile(ctx context.Context, ref ImmutableRef, req ReadRequest) ([]byte, error) {
	var dt []byte

	err := withMount(ctx, ref, func(root string) error {
		fp, err := fs.RootPath(root, req.Filename)
		if err != nil {
			return err
		}

		if req.Range == nil {
			dt, err = ioutil.ReadFile(fp)
			if err != nil {
				return err
			}
		} else {
			f, err := os.Open(fp)
			if err != nil {
				return err
			}
			dt, err = ioutil.ReadAll(io.NewSectionReader(f, int64(req.Range.Offset), int64(req.Range.Length)))
			f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	return dt, err
}
