//go:build !cgo || !(linux || darwin)

package sqlcachestorage

import (
	"database/sql"
	"runtime"

	"github.com/pkg/errors"
)

func sqliteOpen(_ string) (*sql.DB, error) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		return nil, errors.New("sqlite cache storage requires buildkit to be compiled with cgo")
	}
	return nil, errors.Errorf("sqlite cache storage unsupported on %s", runtime.GOOS)
}
