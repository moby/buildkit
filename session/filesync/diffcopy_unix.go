//go:build !windows

package filesync

import (
	pkgerrors "github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
)

func sendDiffCopy(stream Stream, fs fsutil.FS, progress progressCb) error {
	return pkgerrors.WithStack(fsutil.Send(stream.Context(), stream, fs, progress))
}
