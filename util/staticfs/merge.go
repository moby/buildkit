package staticfs

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

type MergeFS struct {
	Lower fsutil.FS
	Upper fsutil.FS
}

var _ fsutil.FS = &MergeFS{}

func NewMergeFS(lower, upper fsutil.FS) *MergeFS {
	return &MergeFS{
		Lower: lower,
		Upper: upper,
	}
}

type record struct {
	path string
	fi   fs.FileInfo
	err  error
}

func (mfs *MergeFS) Walk(ctx context.Context, fn filepath.WalkFunc) error {
	ch1 := make(chan *record, 10)
	ch2 := make(chan *record, 10)

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		defer close(ch1)
		return mfs.Lower.Walk(ctx, func(path string, info fs.FileInfo, err error) error {
			ch1 <- &record{path: path, fi: info, err: err}
			return nil
		})
	})
	eg.Go(func() error {
		defer close(ch2)
		return mfs.Upper.Walk(ctx, func(path string, info fs.FileInfo, err error) error {
			ch2 <- &record{path: path, fi: info, err: err}
			return nil
		})
	})

	eg.Go(func() error {
		next1, ok1 := <-ch1
		next2, ok2 := <-ch2

		for {
			if !ok1 && !ok2 {
				break
			}
			if !ok2 || ok1 && next1.path < next2.path {
				if err := fn(next1.path, next1.fi, next1.err); err != nil {
					return err
				}
				next1, ok1 = <-ch1
			} else if !ok1 || ok2 && next1.path >= next2.path {
				if err := fn(next2.path, next2.fi, next2.err); err != nil {
					return err
				}
				if ok1 && next2.path == next1.path {
					next1, ok1 = <-ch1
				}
				next2, ok2 = <-ch2
			}
		}
		return nil
	})

	return eg.Wait()
}

func (mfs *MergeFS) Open(p string) (io.ReadCloser, error) {
	r, err := mfs.Upper.Open(p)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return mfs.Lower.Open(p)
	}
	return r, nil
}
