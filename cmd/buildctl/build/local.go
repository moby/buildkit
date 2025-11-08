package build

import (
	pkgerrors "github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
)

// ParseLocal parses --local
func ParseLocal(locals []string) (map[string]fsutil.FS, error) {
	localDirs, err := attrMap(locals)
	if err != nil {
		return nil, pkgerrors.WithStack(err)
	}

	mounts := make(map[string]fsutil.FS, len(localDirs))

	for k, v := range localDirs {
		mounts[k], err = fsutil.NewFS(v)
		if err != nil {
			return nil, pkgerrors.WithStack(err)
		}
	}

	return mounts, nil
}
