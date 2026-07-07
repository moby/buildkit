package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

func testLocalSourceDiffer(t *testing.T, sb integration.Sandbox) {
	for _, d := range []llb.DiffType{llb.DiffNone, llb.DiffMetadata} {
		t.Run(fmt.Sprintf("differ=%s", d), func(t *testing.T) {
			testLocalSourceWithDiffer(t, sb, d)
		})
	}
}

func testLocalSourceWithDiffer(t *testing.T, sb integration.Sandbox, d llb.DiffType) {
	c, err := New(context.TODO(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("foo", []byte("foo"), 0600),
	)

	tv := syscall.NsecToTimespec(time.Now().UnixNano())

	err = syscall.UtimesNano(filepath.Join(dir.Name, "foo"), []syscall.Timespec{tv, tv})
	require.NoError(t, err)

	st := llb.Local("mylocal"+string(d), llb.Differ(d, false))

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"mylocal" + string(d): dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("foo"), dt)

	// Give some extra time to ensure the resources from first solve are released
	time.Sleep(500 * time.Millisecond)

	err = os.WriteFile(filepath.Join(dir.Name, "foo"), []byte("bar"), 0600)
	require.NoError(t, err)

	err = syscall.UtimesNano(filepath.Join(dir.Name, "foo"), []syscall.Timespec{tv, tv})
	require.NoError(t, err)

	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"mylocal" + string(d): dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	if d == llb.DiffMetadata {
		require.Equal(t, []byte("foo"), dt)
	}
	if d == llb.DiffNone {
		require.Equal(t, []byte("bar"), dt)
	}
}

// moby/buildkit#4831
func testLocalSourceWithHardlinksFilter(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(context.TODO(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("bar", []byte("bar"), 0600),
		fstest.Link("bar", "foo1"),
		fstest.Link("bar", "foo2"),
	)

	st := llb.Local("mylocal", llb.FollowPaths([]string{"foo*"}))

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(context.TODO(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"mylocal": dir,
		},
	}, nil)
	require.NoError(t, err)

	_, err = os.ReadFile(filepath.Join(destDir, "bar"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	dt, err := os.ReadFile(filepath.Join(destDir, "foo1"))
	require.NoError(t, err)
	require.Equal(t, []byte("bar"), dt)

	st1, err := os.Stat(filepath.Join(destDir, "foo1"))
	require.NoError(t, err)

	st2, err := os.Stat(filepath.Join(destDir, "foo2"))
	require.NoError(t, err)

	require.True(t, os.SameFile(st1, st2))
}

func testLocalSymlinkEscape(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	test := []byte(`set -ex
[[ -L /mount/foo ]]
[[ -L /mount/sub/bar ]]
[[ -L /mount/bax ]]
[[ -f /mount/bay ]]
[[ -f /mount/sub/sub2/file ]]
[[ ! -f /mount/baz ]]
[[ ! -f /mount/etc/passwd ]]
[[ ! -f /mount/etc/group ]]
[[ $(readlink /mount/foo) == "/etc/passwd" ]]
[[ $(readlink /mount/sub/bar) == "../../../etc/group" ]]
`)

	dir := integration.Tmpdir(
		t,
		// point to absolute path that is not part of dir
		fstest.Symlink("/etc/passwd", "foo"),
		fstest.CreateDir("sub", 0700),
		// point outside of the dir
		fstest.Symlink("../../../etc/group", "sub/bar"),
		// regular valid symlink
		fstest.Symlink("bay", "bax"),
		// target for symlink (not requested)
		fstest.CreateFile("bay", []byte{}, 0600),
		// file with many subdirs
		fstest.CreateDir("sub/sub2", 0700),
		fstest.CreateFile("sub/sub2/file", []byte{}, 0600),
		// unused file that shouldn't be included
		fstest.CreateFile("baz", []byte{}, 0600),
		fstest.CreateFile("test.sh", test, 0700),
	)

	local := llb.Local("mylocal", llb.FollowPaths([]string{
		"test.sh", "foo", "sub/bar", "bax", "sub/sub2/file",
	}))

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh /mount/test.sh`), llb.AddMount("/mount", local, llb.Readonly))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			"mylocal": dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMetadataOnlyLocal(t *testing.T, sb integration.Sandbox) {
	ctx, cancel := context.WithCancelCause(sb.Context())
	defer func() { cancel(errors.WithStack(context.Canceled)) }()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	srcDir := integration.Tmpdir(
		t,
		fstest.CreateFile("data", []byte("contents"), 0600),
		fstest.CreateDir("dir", 0700),
		fstest.CreateFile("dir/file1", []byte("file1"), 0600),
		fstest.CreateFile("dir/file2", []byte("file2"), 0600),
		fstest.CreateDir("dir/subdir", 0700),
		fstest.CreateDir("dir/subdir2", 0700),
		fstest.CreateFile("dir/subdir/bar1", []byte("bar1"), 0600),
		fstest.CreateFile("dir/subdir/bar2", []byte("bar2"), 0600),
		fstest.CreateFile("dir/subdir2/bar3", []byte("bar3"), 0600),
		fstest.CreateFile("foo", []byte("foo"), 0602),
	)

	def, err := llb.Local("source", llb.MetadataOnlyTransfer([]string{"dir/**/*1", "foo"})).Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"source": srcDir,
		},
	}, nil)
	require.NoError(t, err)

	_, err = os.ReadFile(filepath.Join(destDir, "data"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	act, err := os.ReadFile(filepath.Join(destDir, "dir/file1"))
	require.NoError(t, err)
	require.Equal(t, "file1", string(act))

	act, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "foo", string(act))

	_, err = os.ReadFile(filepath.Join(destDir, "dir/file2"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	act, err = os.ReadFile(filepath.Join(destDir, "dir/subdir/bar1"))
	require.NoError(t, err)
	require.Equal(t, "bar1", string(act))

	_, err = os.Stat(filepath.Join(destDir, "dir/subdir2"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	_, err = os.ReadFile(filepath.Join(destDir, "dir/subdir/bar2"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	dt, err := os.ReadFile(filepath.Join(destDir, ".fsutil-metadata"))
	require.NoError(t, err)

	stats := parseFSMetadata(t, dt)
	require.Equal(t, 10, len(stats))

	require.Equal(t, "data", stats[0].Path)
	require.Equal(t, "dir", stats[1].Path)
	require.Equal(t, "dir/file1", stats[2].Path)
	require.Equal(t, "dir/file2", stats[3].Path)
	require.Equal(t, "dir/subdir", stats[4].Path)
	require.Equal(t, "dir/subdir/bar1", stats[5].Path)
	require.Equal(t, "dir/subdir/bar2", stats[6].Path)
	require.Equal(t, "dir/subdir2", stats[7].Path)
	require.Equal(t, "dir/subdir2/bar3", stats[8].Path)
	require.Equal(t, "foo", stats[9].Path)

	err = os.RemoveAll(filepath.Join(srcDir.Name, "dir/subdir"))
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(srcDir.Name, "dir/file1"), []byte("file1-updated"), 0600)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(srcDir.Name, "dir/bar1"), []byte("bar1"), 0600)
	require.NoError(t, err)

	def, err = llb.Local("source", llb.MetadataOnlyTransfer([]string{"dir/**/*1", "foo"})).Marshal(sb.Context())
	require.NoError(t, err)

	destDir = t.TempDir()

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"source": srcDir,
		},
	}, nil)
	require.NoError(t, err)

	_, err = os.ReadFile(filepath.Join(destDir, "data"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	act, err = os.ReadFile(filepath.Join(destDir, "dir/file1"))
	require.NoError(t, err)
	require.Equal(t, "file1-updated", string(act))

	act, err = os.ReadFile(filepath.Join(destDir, "dir/bar1"))
	require.NoError(t, err)
	require.Equal(t, "bar1", string(act))

	_, err = os.Stat(filepath.Join(destDir, "dir/subdir"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	dt, err = os.ReadFile(filepath.Join(destDir, ".fsutil-metadata"))
	require.NoError(t, err)

	stats = parseFSMetadata(t, dt)
	require.Equal(t, 8, len(stats))

	require.Equal(t, "data", stats[0].Path)
	require.Equal(t, "dir", stats[1].Path)
	require.Equal(t, "dir/bar1", stats[2].Path)
	require.Equal(t, "dir/file1", stats[3].Path)
	require.Equal(t, "dir/file2", stats[4].Path)
	require.Equal(t, "dir/subdir2", stats[5].Path)
	require.Equal(t, "dir/subdir2/bar3", stats[6].Path)
	require.Equal(t, "foo", stats[7].Path)
}

// moby/buildkit#492
func testParallelLocalBuilds(t *testing.T, sb integration.Sandbox) {
	ctx, cancel := context.WithCancelCause(sb.Context())
	defer func() { cancel(errors.WithStack(context.Canceled)) }()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	eg, ctx := errgroup.WithContext(ctx)

	for i := range 3 {
		func(i int) {
			eg.Go(func() error {
				fn := fmt.Sprintf("test%d", i)
				srcDir := integration.Tmpdir(
					t,
					fstest.CreateFile(fn, []byte("contents"), 0600),
				)

				def, err := llb.Local("source").Marshal(sb.Context())
				require.NoError(t, err)

				destDir := t.TempDir()

				_, err = c.Solve(ctx, def, SolveOpt{
					Exports: []ExportEntry{
						{
							Type:      ExporterLocal,
							OutputDir: destDir,
						},
					},
					LocalMounts: map[string]fsutil.FS{
						"source": srcDir,
					},
				}, nil)
				require.NoError(t, err)

				act, err := os.ReadFile(filepath.Join(destDir, fn))
				require.NoError(t, err)

				require.Equal(t, "contents", string(act))
				return nil
			})
		}(i)
	}

	err = eg.Wait()
	require.NoError(t, err)
}
