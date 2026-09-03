//go:build windows

package contenthash

import (
	"os"

	fstypes "github.com/tonistiigi/fsutil/types"
)

func setUnixOpt(string, os.FileInfo, *fstypes.Stat) error {
	return nil
}
