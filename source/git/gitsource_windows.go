//go:build windows
// +build windows

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// openSubdirSafe on Windows falls back to os.Lstat-based validation since
// unix.Openat is not available.
func openSubdirSafe(root string, subpath string) (*os.File, error) {
	rel := filepath.Clean(subpath)
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	if rel != "" && rel != "." {
		p := ""
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			p = filepath.Join(p, part)
			fi, err := os.Lstat(filepath.Join(root, p))
			if err != nil {
				return nil, errors.Wrapf(err, "failed to lstat %q", p)
			}
			if !fi.IsDir() {
				return nil, errors.Errorf("git subpath %q contains non-directory %q", subpath, p)
			}
		}
	}
	return os.Open(filepath.Join(root, subpath))
}

// readdirnames on Windows uses a normal readable fd returned by openSubdirSafe.
func readdirnames(f *os.File) ([]string, error) {
	return f.Readdirnames(0)
}

func runWithStandardUmask(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
		case <-waitDone:
		}
	}()
	return cmd.Wait()
}
