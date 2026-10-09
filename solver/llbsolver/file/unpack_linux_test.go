//go:build linux

package file

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	libcap "kernel.org/pub/linux/libs/security/libcap/cap"
)

func TestRootMetadataWithoutProc(t *testing.T) {
	const childRootEnv = "BUILDKIT_TEST_METADATA_WITHOUT_PROC"
	jail := os.Getenv(childRootEnv)
	if jail == "" {
		if os.Geteuid() != 0 {
			t.Skip("requires root to chroot the test subprocess")
		}
		jail = t.TempDir()
		// The unprivileged child must be able to traverse its root and write /tmp.
		require.NoError(t, os.Chmod(jail, 0o755))
		require.NoError(t, os.Mkdir(filepath.Join(jail, "tmp"), 0o777))
		require.NoError(t, os.Chmod(filepath.Join(jail, "tmp"), 0o777))
		exe, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestRootMetadataWithoutProc$", "-test.v", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), childRootEnv+"="+jail, "TMPDIR=/tmp")
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 77 {
			t.Skipf("subprocess cannot isolate filesystem or drop privileges: %s", out)
		}
		require.NoError(t, err, "%s", out)
		t.Logf("%s", out)
		return
	}
	// Confine only the subprocess; the parent's filesystem and credentials stay intact.
	if err := syscall.Chroot(jail); errors.Is(err, syscall.EPERM) {
		os.Exit(77)
	} else {
		require.NoError(t, err)
	}
	require.NoError(t, os.Chdir("/")) //nolint:usetesting // t.Chdir would retain and restore a directory outside the chroot.
	for _, drop := range []func() error{
		func() error { return syscall.Setgroups(nil) },
		func() error { return syscall.Setgid(1000) },
		func() error { return syscall.Setuid(1000) },
	} {
		err := drop()
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) {
			os.Exit(77)
		}
		require.NoError(t, err)
	}
	_, err := os.Stat("/proc")
	require.ErrorIs(t, err, os.ErrNotExist)
	dest := t.TempDir()
	root, err := os.OpenRoot(dest)
	require.NoError(t, err)
	defer root.Close()
	file := filepath.Join(dest, "file")
	require.NoError(t, os.WriteFile(file, []byte("unchanged"), 0o200))
	require.NoError(t, root.Symlink("file", "link"))
	parent, err := root.Open(".")
	require.NoError(t, err)
	defer parent.Close()
	for _, tc := range []struct {
		name string
		op   string
		err  error
	}{
		{name: "link", op: "lsetxattr", err: setRootSymlinkXattr(root, "link", "user.buildkit", []byte("value"))},
		{name: "file", op: "setxattr", err: setRootXattr(root, nil, "file", "user.buildkit", []byte("value"))},
		{name: "file", op: "chmod", err: chmodRootFallback(parent, "file", 0o777)},
	} {
		t.Run(tc.op, func(t *testing.T) {
			require.ErrorIs(t, tc.err, os.ErrNotExist)
			require.ErrorContains(t, tc.err, "cannot restore archive metadata through /proc/self/fd")
			var pathErr *os.PathError
			require.ErrorAs(t, tc.err, &pathErr)
			require.Equal(t, tc.op, pathErr.Op)
			require.Equal(t, tc.name, pathErr.Path)
		})
	}
	fi, err := root.Stat("file")
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o200), fi.Mode().Perm())
	require.NoError(t, os.Chmod(file, 0o600))
	for _, name := range []string{file, filepath.Join(dest, "link")} {
		_, err := unix.Lgetxattr(name, "user.buildkit", nil)
		require.True(t, errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP), "unexpected xattr after failed restoration at %s: %v", name, err)
	}
	contents, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(contents))
}

func TestRootModeFallbackWithoutReadPermission(t *testing.T) {
	for _, kind := range []string{"directory", "fifo", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dest := t.TempDir()
			name := filepath.Join(dest, "entry")
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(name, 0o300))
			case "fifo":
				require.NoError(t, unix.Mkfifo(name, 0o200))
			case "hardlink":
				source := filepath.Join(dest, "source")
				require.NoError(t, os.WriteFile(source, []byte("content"), 0o200))
				require.NoError(t, os.Link(source, name))
			}
			t.Cleanup(func() { require.NoError(t, os.Chmod(name, 0o700)) })
			fd, err := unix.Open(name, unix.O_RDONLY|unix.O_NONBLOCK, 0)
			if err == nil {
				unix.Close(fd)
				t.Skip("read permissions are bypassed; run without CAP_DAC_OVERRIDE/CAP_DAC_READ_SEARCH")
			}
			require.ErrorIs(t, err, unix.EACCES, "fixture must reject the old read-only open")
			root, err := os.OpenRoot(dest)
			require.NoError(t, err)
			defer root.Close()
			parent, err := root.Open(".")
			require.NoError(t, err)
			defer parent.Close()

			// Call the fallback directly so modern kernels also exercise it.
			require.NoError(t, chmodRootFallback(parent, "entry", 0o700|unix.S_ISVTX))
			fi, err := root.Stat("entry")
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
			require.NotZero(t, fi.Mode()&os.ModeSticky)
			if kind == "hardlink" {
				source, err := root.Stat("source")
				require.NoError(t, err)
				require.True(t, os.SameFile(fi, source))
				require.Equal(t, fi.Mode(), source.Mode())
			}
		})
	}
}

func TestRootModeFallbackRejectsSymlinks(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	require.NoError(t, os.Mkdir(dest, 0o700))
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "inside"), []byte("inside"), 0o600))
	root, err := os.OpenRoot(dest)
	require.NoError(t, err)
	defer root.Close()
	parent, err := root.Open(".")
	require.NoError(t, err)
	defer parent.Close()
	for _, target := range []string{"inside", "../outside", outside, "missing", "link"} {
		t.Run(target, func(t *testing.T) {
			require.NoError(t, root.Symlink(target, "link"))
			defer root.Remove("link")
			require.ErrorIs(t, chmodRootFallback(parent, "link", 0o777), unix.ELOOP)
			for _, name := range []string{outside, filepath.Join(dest, "inside")} {
				fi, err := os.Stat(name)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
			}
		})
	}
	require.ErrorIs(t, chmodRootFallback(parent, "missing", 0o777), os.ErrNotExist)
}

func TestUnpackRestoresXattrsWithoutReadPermission(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		name := "new directory"
		if preexisting {
			name = "existing directory"
		}
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			const key = "user.buildkit.dir"
			if err := unix.Setxattr(dest, key, []byte("probe"), 0); err != nil {
				if isBestEffortRootXattrError(err) {
					t.Skipf("user xattrs are not supported: %v", err)
				}
				require.NoError(t, err)
			}
			dir := filepath.Join(dest, "dir")
			if preexisting {
				require.NoError(t, os.Mkdir(dir, 0o300))
			}
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
			buf := &bytes.Buffer{}
			tw := tar.NewWriter(buf)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name:       "dir",
				Typeflag:   tar.TypeDir,
				Mode:       0o300,
				PAXRecords: map[string]string{"SCHILY.xattr." + key: "dir-value"},
			}))
			require.NoError(t, tw.Close())

			require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
			fi, err := os.Stat(dir)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o300), fi.Mode().Perm())
			require.NoError(t, os.Chmod(dir, 0o700))
			value := make([]byte, 128)
			n, err := unix.Lgetxattr(dir, key, value)
			require.NoError(t, err)
			require.Equal(t, "dir-value", string(value[:n]))
		})
	}
}

func TestUnpackRestoresHardlinkXattrsWithoutReadPermission(t *testing.T) {
	dest := t.TempDir()
	source := filepath.Join(dest, "source")
	require.NoError(t, os.WriteFile(source, []byte("content"), 0o200))
	const key = "user.buildkit.file"
	if err := unix.Setxattr(source, key, []byte("before"), 0); err != nil {
		if isBestEffortRootXattrError(err) {
			t.Skipf("user xattrs are not supported: %v", err)
		}
		require.NoError(t, err)
	}
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:       "hardlink",
		Typeflag:   tar.TypeLink,
		Linkname:   "source",
		Mode:       0o200,
		PAXRecords: map[string]string{"SCHILY.xattr." + key: "after"},
	}))
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
	fi, err := os.Stat(source)
	require.NoError(t, err)
	hi, err := os.Stat(filepath.Join(dest, "hardlink"))
	require.NoError(t, err)
	require.True(t, os.SameFile(fi, hi))
	require.Equal(t, os.FileMode(0o200), fi.Mode().Perm())
	require.NoError(t, os.Chmod(source, 0o600))
	value := make([]byte, 128)
	n, err := unix.Lgetxattr(source, key, value)
	require.NoError(t, err)
	require.Equal(t, "after", string(value[:n]))
}

func TestRootXattrRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	require.NoError(t, os.Mkdir(dest, 0o700))
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("content"), 0o200))
	const key = "user.buildkit.file"
	if err := unix.Setxattr(outside, key, []byte("before"), 0); err != nil {
		if isBestEffortRootXattrError(err) {
			t.Skipf("user xattrs are not supported: %v", err)
		}
		require.NoError(t, err)
	}
	require.NoError(t, os.Symlink("../outside", filepath.Join(dest, "leaf")))
	require.NoError(t, os.Symlink("..", filepath.Join(dest, "parent")))
	root, err := os.OpenRoot(dest)
	require.NoError(t, err)
	defer root.Close()
	for _, name := range []string{"leaf", "parent/outside"} {
		require.Error(t, setRootXattr(root, nil, name, key, []byte("after")))
	}
	require.NoError(t, os.Chmod(outside, 0o600))
	value := make([]byte, 128)
	n, err := unix.Lgetxattr(outside, key, value)
	require.NoError(t, err)
	require.Equal(t, "before", string(value[:n]))
}

func TestUnpackRestoresFileCapability(t *testing.T) {
	dest := t.TempDir()
	probe := filepath.Join(dest, "probe")
	require.NoError(t, os.WriteFile(probe, []byte("probe"), 0o600))
	caps, err := libcap.FromText("cap_net_bind_service=ep")
	require.NoError(t, err)
	if err := caps.SetFile(probe); err != nil {
		if isBestEffortRootXattrError(err) {
			t.Skipf("file capabilities are unavailable: %v", err)
		}
		require.NoError(t, err)
	}
	const key = "security.capability"
	value := make([]byte, 64)
	n, err := unix.Getxattr(probe, key, value)
	require.NoError(t, err)
	value = value[:n]
	// Confirm that this environment makes chown-after-xattr observable.
	require.NoError(t, os.Chown(probe, os.Getuid(), os.Getgid()))
	_, err = unix.Getxattr(probe, key, nil)
	require.ErrorIs(t, err, unix.ENODATA)

	const content = "content"
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:       "file",
		Typeflag:   tar.TypeReg,
		Mode:       0o644,
		Size:       int64(len(content)),
		Uid:        os.Getuid(),
		Gid:        os.Getgid(),
		PAXRecords: map[string]string{"SCHILY.xattr." + key: string(value)},
	}))
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, applyRootArchive(t.Context(), dest, &buf, nil, nil, false))
	name := filepath.Join(dest, "file")
	restored, err := libcap.GetFile(name)
	require.NoError(t, err)
	require.Equal(t, caps.String(), restored.String())
	dt, err := os.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, content, string(dt))
}

func TestUnpackRestoresUserXattrs(t *testing.T) {
	dest := t.TempDir()
	probe := filepath.Join(dest, "probe")
	require.NoError(t, os.WriteFile(probe, []byte("probe"), 0o644))
	if err := unix.Lsetxattr(probe, "user.buildkit.probe", []byte("ok"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPERM) {
			t.Skipf("user xattrs are not supported on this filesystem: %v", err)
		}
		require.NoError(t, err)
	}
	require.NoError(t, os.Remove(probe))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dir",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		PAXRecords: map[string]string{
			"SCHILY.xattr.user.buildkit.dir": "dir-value",
		},
	}))
	content := "content"
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dir/file",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(content)),
		PAXRecords: map[string]string{
			"SCHILY.xattr.user.buildkit.file": "file-value",
		},
	}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	xattrValue := make([]byte, 128)
	n, err := unix.Lgetxattr(filepath.Join(dest, "dir"), "user.buildkit.dir", xattrValue)
	require.NoError(t, err)
	require.Equal(t, "dir-value", string(xattrValue[:n]))

	n, err = unix.Lgetxattr(filepath.Join(dest, "dir", "file"), "user.buildkit.file", xattrValue)
	require.NoError(t, err)
	require.Equal(t, "file-value", string(xattrValue[:n]))
}

func TestUnpackFIFOXattrs(t *testing.T) {
	const key = "user.buildkit.fifo"
	for _, tc := range []struct {
		name string
		mode int64
	}{
		{name: "readable", mode: 0o600},
		{name: "write-only", mode: 0o200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			probe := filepath.Join(dest, "probe")
			require.NoError(t, unix.Mkfifo(probe, 0o600))
			probeErr := unix.Lsetxattr(probe, key, []byte("value"), 0)
			if probeErr != nil {
				require.True(t, isBestEffortRootXattrError(probeErr), "unexpected xattr error: %v", probeErr)
			}
			buf := bytes.NewBuffer(nil)
			tw := tar.NewWriter(buf)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name:       "pipe",
				Typeflag:   tar.TypeFifo,
				Mode:       tc.mode,
				PAXRecords: map[string]string{"SCHILY.xattr." + key: "value"},
			}))
			require.NoError(t, tw.Close())
			// Even when FIFO xattrs are unsupported, extraction must finish without
			// blocking on the pipe or failing on the best-effort xattr error.
			require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
			name := filepath.Join(dest, "pipe")
			fi, err := os.Lstat(name)
			require.NoError(t, err)
			require.NotZero(t, fi.Mode()&os.ModeNamedPipe)
			require.Equal(t, os.FileMode(tc.mode), fi.Mode().Perm())
			if probeErr == nil {
				require.NoError(t, os.Chmod(name, 0o600))
				value := make([]byte, 128)
				n, err := unix.Lgetxattr(name, key, value)
				require.NoError(t, err)
				require.Equal(t, "value", string(value[:n]))
			}
		})
	}
}

func TestUnpackSELinuxXattrOnDanglingSymlink(t *testing.T) {
	const key = "security.selinux"
	const label = "system_u:object_r:container_file_t:s0"
	probe := filepath.Join(t.TempDir(), "probe")
	require.NoError(t, os.Symlink("usr/bin", probe))
	if err := unix.Lsetxattr(probe, key, []byte(label), 0); err != nil {
		// The host SELinux policy may reject relabeling or not recognize this label.
		if isBestEffortRootXattrError(err) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.EINVAL) {
			t.Skipf("SELinux labels on symlinks are unavailable: %v", err)
		}
		require.NoError(t, err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// The labeled symlink precedes its target, which does not exist yet.
	for _, hdr := range []tar.Header{
		{
			Name:       "bin",
			Typeflag:   tar.TypeSymlink,
			Linkname:   "usr/bin",
			Mode:       0o777,
			PAXRecords: map[string]string{"SCHILY.xattr." + key: label},
		},
		{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "usr/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
	} {
		require.NoError(t, tw.WriteHeader(&hdr))
	}
	require.NoError(t, tw.Close())

	dest := t.TempDir()
	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
	link := filepath.Join(dest, "bin")
	target, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, "usr/bin", target)
	fi, err := os.Stat(filepath.Join(dest, "usr", "bin"))
	require.NoError(t, err)
	require.True(t, fi.IsDir())
	value := make([]byte, 256)
	n, err := unix.Lgetxattr(link, key, value)
	require.NoError(t, err)
	require.Equal(t, label, string(bytes.TrimSuffix(value[:n], []byte{0})))
}

func TestUnpackSymlinkXattrs(t *testing.T) {
	for _, key := range []string{"user.buildkit.link", "trusted.buildkit.link"} {
		t.Run(key, func(t *testing.T) {
			for _, targetKind := range []string{"inside", "outside", "dangling", "loop"} {
				t.Run(targetKind, func(t *testing.T) {
					dest := t.TempDir()
					target := filepath.Join(dest, "target")
					linkTarget := "target"
					switch targetKind {
					case "outside":
						target = filepath.Join(t.TempDir(), "target")
						linkTarget = target
					case "dangling":
						linkTarget = "missing"
					case "loop":
						linkTarget = "link"
					}
					require.NoError(t, os.WriteFile(target, []byte("content"), 0o600))
					if err := unix.Lsetxattr(target, key, []byte("before"), 0); err != nil {
						if isBestEffortRootXattrError(err) {
							t.Skipf("xattr namespace unavailable: %v", err)
						}
						require.NoError(t, err)
					}
					probe := filepath.Join(dest, "probe")
					require.NoError(t, os.Symlink("target", probe))
					probeErr := unix.Lsetxattr(probe, key, []byte("probe"), 0)
					if probeErr != nil {
						require.True(t, isBestEffortRootXattrError(probeErr), "unexpected symlink xattr error: %v", probeErr)
						t.Logf("checking best-effort behavior: symlink xattrs unavailable: %v", probeErr)
					} else {
						t.Log("checking successful symlink xattr restoration")
					}
					buf := bytes.NewBuffer(nil)
					tw := tar.NewWriter(buf)
					require.NoError(t, tw.WriteHeader(&tar.Header{
						Name:       "link",
						Typeflag:   tar.TypeSymlink,
						Linkname:   linkTarget,
						Mode:       0o777,
						PAXRecords: map[string]string{"SCHILY.xattr." + key: "link-value"},
					}))
					require.NoError(t, tw.Close())
					require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
					value := make([]byte, 128)
					if probeErr == nil {
						n, err := unix.Lgetxattr(filepath.Join(dest, "link"), key, value)
						require.NoError(t, err)
						require.Equal(t, "link-value", string(value[:n]))
					}

					buf.Reset()
					tw = tar.NewWriter(buf)
					require.NoError(t, tw.WriteHeader(&tar.Header{
						Name:       "alias",
						Typeflag:   tar.TypeLink,
						Linkname:   "link",
						Mode:       0o777,
						PAXRecords: map[string]string{"SCHILY.xattr." + key: "alias-value"},
					}))
					require.NoError(t, tw.Close())
					require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
					for _, name := range []string{"link", "alias"} {
						got, err := os.Readlink(filepath.Join(dest, name))
						require.NoError(t, err)
						require.Equal(t, linkTarget, got)
						if probeErr == nil {
							n, err := unix.Lgetxattr(filepath.Join(dest, name), key, value)
							require.NoError(t, err)
							require.Equal(t, "alias-value", string(value[:n]))
						}
					}
					n, err := unix.Lgetxattr(target, key, value)
					require.NoError(t, err)
					require.Equal(t, "before", string(value[:n]))
				})
			}
		})
	}
}

func TestRootSymlinkXattrRejectsParentEscape(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(dest, 0o700))
	require.NoError(t, os.Mkdir(outside, 0o700))
	require.NoError(t, os.Symlink("missing", filepath.Join(outside, "link")))
	require.NoError(t, os.Symlink("../outside", filepath.Join(dest, "escape")))
	root, err := os.OpenRoot(dest)
	require.NoError(t, err)
	defer root.Close()
	for _, name := range []string{"../outside/link", "escape/link"} {
		require.Error(t, setRootSymlinkXattr(root, name, "user.buildkit.link", []byte("value")))
	}
	// Non-best-effort failures must not be silently swallowed.
	require.NoError(t, os.Symlink("missing", filepath.Join(dest, "link")))
	require.Error(t, setRootSymlinkXattr(root, "link", "invalid\x00key", []byte("value")))
}
