//go:build !windows

package openfile

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRegular(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(p, []byte("contents"), 0644))

	f, err := Regular(p)
	require.NoError(t, err)
	defer f.Close()

	dt, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Equal(t, []byte("contents"), dt)
}

func TestRejectsSpecialFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		create func(t *testing.T, p string)
	}{
		{"fifo", func(t *testing.T, p string) {
			require.NoError(t, unix.Mkfifo(p, 0600))
		}},
		{"socket", func(t *testing.T, p string) {
			var lc net.ListenConfig
			l, err := lc.Listen(t.Context(), "unix", p)
			require.NoError(t, err)
			t.Cleanup(func() { l.Close() })
		}},
		{"directory", func(t *testing.T, p string) {
			require.NoError(t, os.Mkdir(p, 0755))
		}},
		{"symlink", func(t *testing.T, p string) {
			// Regular takes an already-resolved path, so a symlink appearing at
			// it means the path was swapped after resolution.
			require.NoError(t, os.WriteFile(p+".target", []byte("x"), 0644))
			require.NoError(t, os.Symlink(p+".target", p))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "special")
			tc.create(t, p)
			requireRejected(t, p)
		})
	}
}

// requireRejected fails if Regular accepts p, and does not wait forever: a fifo
// with no writer would block an unfixed implementation in open(2).
func requireRejected(t *testing.T, p string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		f, err := Regular(p)
		if err == nil {
			f.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, ErrNotRegular)
	case <-time.After(20 * time.Second):
		require.Fail(t, "Regular blocked on a special file")
	}
}
