//go:build !cgo || !(linux || darwin)

package sqlitecachestorage

import (
	"database/sql"
	"runtime"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/pkg/errors"
)

func sqliteOpen(_ string) (*sql.DB, error) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		return nil, errors.Wrap(cerrdefs.ErrNotImplemented, "sqlite cache storage requires buildkit to be compiled with cgo")
	}
	return nil, errors.Wrapf(cerrdefs.ErrNotImplemented, "sqlite cache storage unsupported on %s", runtime.GOOS)
}
