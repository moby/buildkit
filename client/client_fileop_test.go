package client

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testCopyFromEmptyImage(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// On Windows, the error messages are different for the Scratch image
	// (which is coming from the OS). While the one for the no-layers image
	// is being returned by Buildkit (in unix style).
	winErrMsgs := []string{
		"foo: The system cannot find the file specified", // for llb.Scratch()
		"/foo: no such file or directory",                // for tonistiigi/test:nolayers
	}

	for i, image := range []llb.State{llb.Scratch(), llb.Image("tonistiigi/test:nolayers")} {
		st := llb.Scratch().File(llb.Copy(image, "/", "/"))
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.NoError(t, err)

		st = llb.Scratch().File(llb.Copy(image, "/foo", "/"))
		def, err = st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.Error(t, err)
		errMsg := integration.UnixOrWindows(
			"/foo: no such file or directory",
			winErrMsgs[i],
		)
		require.Contains(t, err.Error(), errMsg)

		imgName := integration.UnixOrWindows(
			"busybox:latest",
			"nanoserver:latest",
		)
		busybox := llb.Image(imgName)

		cmdStr := integration.UnixOrWindows(
			`sh -e -c '[ $(ls /scratch | wc -l) = '0' ]'`,
			`cmd /C dir \scratch`,
		)
		out := busybox.Run(llb.Shlex(cmdStr))
		out.AddMount("/scratch", image, llb.Readonly)

		def, err = out.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.NoError(t, err)
	}
}

// containerd/containerd#2119
func testDuplicateWhiteouts(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -e -c "mkdir -p d0 d1; echo -n first > d1/bar;"`)
	run(`sh -c "rm -rf d0 d1"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]

	layer, ok := m[ocispecs.ImageBlobsDir+"/sha256/"+lastLayer.Digest.Hex()]
	require.True(t, ok)

	m, err = testutil.ReadTarToMap(layer.Data, true)
	require.NoError(t, err)

	_, ok = m[".wh.d0"]
	require.True(t, ok)

	_, ok = m[".wh.d1"]
	require.True(t, ok)

	// check for a bug that added whiteout for subfile
	_, ok = m["d1/.wh.bar"]
	require.True(t, !ok)
}

func testFileOpCopyAlwaysReplaceExistingDestPaths(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDirHostPath := integration.Tmpdir(t,
		fstest.CreateDir("root", 0755),
		fstest.CreateDir("root/overwritedir", 0755),
		fstest.CreateFile("root/overwritedir/subfile", nil, 0755),
		fstest.CreateFile("root/overwritefile", nil, 0755),
		fstest.Symlink("dir", "root/overwritesymlink"),
		fstest.CreateDir("root/dir", 0755),
		fstest.CreateFile("root/dir/dirfile1", nil, 0755),
		fstest.CreateDir("root/dir/overwritesubdir", 0755),
		fstest.CreateFile("root/dir/overwritesubfile", nil, 0755),
		fstest.Symlink("dirfile1", "root/dir/overwritesymlink"),
	)
	destDir := llb.Local("destDir")

	srcDirHostPath := integration.Tmpdir(t,
		fstest.CreateDir("root", 0755),
		fstest.CreateFile("root/overwritedir", nil, 0755),
		fstest.CreateDir("root/overwritefile", 0755),
		fstest.CreateFile("root/overwritefile/foo", nil, 0755),
		fstest.CreateDir("root/overwritesymlink", 0755),
		fstest.CreateDir("root/dir", 0755),
		fstest.CreateFile("root/dir/dirfile2", nil, 0755),
		fstest.CreateFile("root/dir/overwritesubdir", nil, 0755),
		fstest.CreateDir("root/dir/overwritesubfile", 0755),
		fstest.CreateDir("root/dir/overwritesymlink", 0755),
	)
	srcDir := llb.Local("srcDir")

	resultDir := destDir.File(llb.Copy(srcDir, "/", "/", &llb.CopyInfo{
		CopyDirContentsOnly:            true,
		AlwaysReplaceExistingDestPaths: true,
	}))

	def, err := resultDir.Marshal(sb.Context())
	require.NoError(t, err)

	resultDirHostPath := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: resultDirHostPath,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"destDir": destDirHostPath,
			"srcDir":  srcDirHostPath,
		},
	}, nil)
	require.NoError(t, err)

	err = fstest.CheckDirectoryEqualWithApplier(resultDirHostPath, fstest.Apply(
		fstest.CreateDir("root", 0755),
		fstest.CreateFile("root/overwritedir", nil, 0755),
		fstest.CreateDir("root/overwritefile", 0755),
		fstest.CreateFile("root/overwritefile/foo", nil, 0755),
		fstest.CreateDir("root/overwritesymlink", 0755),
		fstest.CreateDir("root/dir", 0755),
		fstest.CreateFile("root/dir/dirfile1", nil, 0755),
		fstest.CreateFile("root/dir/dirfile2", nil, 0755),
		fstest.CreateFile("root/dir/overwritesubdir", nil, 0755),
		fstest.CreateDir("root/dir/overwritesubfile", 0755),
		fstest.CreateDir("root/dir/overwritesymlink", 0755),
	))
	require.NoError(t, err)
}

func testFileOpCopyChmodText(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	tcases := []struct {
		src  string
		dest string
		mode string
	}{
		{"file", "f1", "go-w"},
		{"file", "f2", "o-rwx,g+x"},
		{"file", "f3", "u=rwx,g=,o="},
		{"file", "f4", "u+rw,g+r,o-x,o+w"},
		{"dir", "d1", "a+X"},
		{"dir", "d2", "g+rw,o+rw"},
		{"dir", "d3", "u=rwX,go=rX"},
	}

	st := llb.Image("alpine").
		Run(llb.Shlex(`sh -c "mkdir /input && touch /input/file && mkdir /input/dir && chmod 0500 /input/dir && mkdir /expected"`))

	for _, tc := range tcases {
		st = st.Run(llb.Shlex(`sh -c "cp -a /input/` + tc.src + ` /expected/` + tc.dest + ` && chmod ` + tc.mode + ` /expected/` + tc.dest + `"`))
	}
	cp := llb.Scratch()

	for _, tc := range tcases {
		cp = cp.File(llb.Copy(st.Root(), "/input/"+tc.src, "/"+tc.dest, llb.ChmodOpt{ModeStr: tc.mode}))
	}

	for _, tc := range tcases {
		st = st.Run(llb.Shlex(`sh -c '[ "$(stat -c '%A' /expected/`+tc.dest+`)" == "$(stat -c '%A' /actual/`+tc.dest+`)" ]'`), llb.AddMount("/actual", cp, llb.Readonly))
	}

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testFileOpCopyIncludeExclude(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("myfile", []byte("data0"), 0600),
		fstest.CreateDir("sub", 0700),
		fstest.CreateFile("sub/foo", []byte("foo0"), 0600),
		fstest.CreateFile("sub/bar", []byte("bar0"), 0600),
	)

	st := llb.Scratch().File(
		llb.Copy(
			llb.Local("mylocal"), "/", "/", &llb.CopyInfo{
				IncludePatterns: []string{"sub/*"},
				ExcludePatterns: []string{"sub/bar"},
			},
		),
	)

	busybox := llb.Image("busybox:latest")
	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}
	run(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
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

	dt, err := os.ReadFile(filepath.Join(destDir, "sub", "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("foo0"), dt)

	for _, name := range []string{"myfile", "sub/bar"} {
		_, err = os.Stat(filepath.Join(destDir, name))
		require.ErrorIs(t, err, os.ErrNotExist)
	}

	randBytes, err := os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	// Create additional file which doesn't match the include pattern, and make
	// sure this doesn't invalidate the cache.

	err = fstest.Apply(fstest.CreateFile("unmatchedfile", []byte("data1"), 0600)).Apply(dir.Name)
	require.NoError(t, err)

	st = llb.Scratch().File(
		llb.Copy(
			llb.Local("mylocal"), "/", "/", &llb.CopyInfo{
				IncludePatterns: []string{"sub/*"},
				ExcludePatterns: []string{"sub/bar"},
			},
		),
	)

	run(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`)

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir = t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
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

	randBytes2, err := os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	require.Equal(t, randBytes, randBytes2)
}

func testFileOpCopyRm(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("myfile", []byte("data0"), 0600),
		fstest.CreateDir("sub", 0700),
		fstest.CreateFile("sub/foo", []byte("foo0"), 0600),
		fstest.CreateFile("sub/bar", []byte("bar0"), 0600),
	)
	dir2 := integration.Tmpdir(
		t,
		fstest.CreateFile("file2", []byte("file2"), 0600),
	)

	st := llb.Scratch().
		File(
			llb.Copy(llb.Local("mylocal"), "myfile", "myfile2").
				Copy(llb.Local("mylocal"), "sub", "out").
				Rm("out/foo").
				Copy(llb.Local("mylocal2"), "file2", "/"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			"mylocal":  dir,
			"mylocal2": dir2,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "myfile2"))
	require.NoError(t, err)
	require.Equal(t, []byte("data0"), dt)

	fi, err := os.Stat(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, true, fi.IsDir())

	dt, err = os.ReadFile(filepath.Join(destDir, "out/bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("bar0"), dt)

	_, err = os.Stat(filepath.Join(destDir, "out/foo"))
	require.ErrorIs(t, err, os.ErrNotExist)

	dt, err = os.ReadFile(filepath.Join(destDir, "file2"))
	require.NoError(t, err)
	require.Equal(t, []byte("file2"), dt)
}

// moby/buildkit#3291
func testFileOpCopyUIDCache(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().File(
		llb.Copy(llb.Image("alpine").Run(llb.Shlex(`sh -c 'echo 123 > /foo && chown 1000:1000 /foo'`)).Root(), "foo", "foo"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	var buf bytes.Buffer
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(&iohelper.NopWriteCloser{Writer: &buf}),
			},
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	fi, ok := m["foo"]
	require.True(t, ok)
	require.Equal(t, 1000, fi.Header.Uid)
	require.Equal(t, 1000, fi.Header.Gid)

	// repeat to check cache does not apply for different uid
	st = llb.Scratch().File(
		llb.Copy(llb.Image("alpine").Run(llb.Shlex(`sh -c 'echo 123 > /foo'`)).Root(), "foo", "foo"))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	buf = bytes.Buffer{}
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(&iohelper.NopWriteCloser{Writer: &buf}),
			},
		},
	}, nil)
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	fi, ok = m["foo"]
	require.True(t, ok)
	require.Equal(t, 0, fi.Header.Uid)
	require.Equal(t, 0, fi.Header.Gid)
}

// testFileOpInputSwap is a regression test that cache is invalidated when subset of fileop is built
func testFileOpInputSwap(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	base := llb.Scratch().File(llb.Mkfile("/foo", 0600, []byte("foo")))

	src := llb.Scratch().File(llb.Mkfile("/bar", 0600, []byte("bar")))

	st := base.File(llb.Copy(src, "/bar", "/baz"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// bar does not exist in base but index of all inputs remains the same
	st = base.File(llb.Copy(base, "/bar", "/baz"))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	errStr := integration.UnixOrWindows(
		"bar: no such file",
		"bar: The system cannot find the file specified",
	)
	require.Contains(t, err.Error(), errStr)
}

func testFileOpMkdirMkfile(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().
		File(llb.Mkdir("/foo", 0700).Mkfile("bar", 0600, []byte("contents")))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	fi, err := os.Stat(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, true, fi.IsDir())

	dt, err := os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, []byte("contents"), dt)
}

func testFileOpRmWildcard(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := integration.Tmpdir(
		t,
		fstest.CreateDir("foo", 0700),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("foo/target", []byte("foo0"), 0600),
		fstest.CreateFile("bar/target", []byte("bar0"), 0600),
		fstest.CreateFile("bar/remaining", []byte("bar1"), 0600),
	)

	st := llb.Scratch().File(
		llb.Copy(llb.Local("mylocal"), "foo", "foo").
			Copy(llb.Local("mylocal"), "bar", "bar"),
	).File(
		llb.Rm("*/target", llb.WithAllowWildcard(true)),
	)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
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

	dt, err := os.ReadFile(filepath.Join(destDir, "bar/remaining"))
	require.NoError(t, err)
	require.Equal(t, []byte("bar1"), dt)

	fi, err := os.Stat(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, true, fi.IsDir())

	_, err = os.Stat(filepath.Join(destDir, "foo/target"))
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = os.Stat(filepath.Join(destDir, "bar/target"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func testFileOpSymlink(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	const (
		fileOwner = 7777
		fileGroup = 8888
		linkOwner = 1111
		linkGroup = 2222

		dummyTimestamp = 42
	)

	dummyTime := time.Unix(dummyTimestamp, 0)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Scratch().
		File(llb.Mkdir("/foo", 0700).Mkfile("bar", 0600, []byte("contents"), llb.ChownOpt{
			User: &llb.UserOpt{
				UID: fileOwner,
			},
			Group: &llb.UserOpt{
				UID: fileGroup,
			},
		})).
		File(llb.Symlink("bar", "/baz", llb.WithCreatedTime(dummyTime), llb.ChownOpt{
			User: &llb.UserOpt{
				UID: linkOwner,
			},
			Group: &llb.UserOpt{
				UID: linkGroup,
			},
		}))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	defer outW.Close()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)
	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	entry, ok := m["bar"]
	require.True(t, ok)

	dt = entry.Data
	header := entry.Header
	require.NoError(t, err)

	require.Equal(t, []byte("contents"), dt)
	require.Equal(t, fileOwner, header.Uid)
	require.Equal(t, fileGroup, header.Gid)

	entry, ok = m["baz"]
	require.Equal(t, true, ok)

	header = entry.Header
	// ensure it is a symlink
	require.Equal(t, tar.TypeSymlink, rune(header.Typeflag))
	// ensure it is a symlink to the proper location
	require.Equal(t, "bar", header.Linkname)

	// make sure it was chowned properly
	require.Equal(t, linkOwner, header.Uid)
	require.Equal(t, linkGroup, header.Gid)

	// ensure it was timestamped properly
	require.Equal(t, dummyTime, header.ModTime)
}

// #2490
func testMoveParentDir(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := llb.Scratch()

	switch imgName {
	case "nanoserver:latest":
		run := func(cmd string) {
			st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).Root()
		}

		run(`cmd /c "mkdir foo && echo first > foo/bar && move foo foo2"`)
	case "busybox:latest":
		run := func(cmd string) {
			st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
		}

		run(`sh -c "mkdir -p foo; echo -n first > foo/bar;"`)
		run(`mv foo foo2`)
	}

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]

	layer, ok := m[ocispecs.ImageBlobsDir+"/sha256/"+lastLayer.Digest.Hex()]
	require.True(t, ok)

	m, err = testutil.ReadTarToMap(layer.Data, true)
	require.NoError(t, err)

	switch imgName {
	case "nanoserver:latest":
		_, ok = m["/wd/foo/bar"] // Should be false, else move didn't happen
		require.False(t, ok)

		_, ok = m["Files/wd/foo/bar"] // Should be false, else move didn't happen
		require.False(t, ok)

		_, ok = m["Files/wd/foo2/bar"]
		require.True(t, ok)

		_, ok = m["Files/wd/foo2"]
		require.True(t, ok)

		_, ok = m["Files/wd"]
		require.True(t, ok)

		for key := range m {
			if err == nil && strings.Contains(key, "/wd") {
				ok = true
				break
			} else {
				ok = false
			}
		}
		require.True(t, ok)

		for key := range m {
			if err == nil && strings.Contains(key, "/foo2/bar") {
				ok = true
				break
			} else {
				ok = false
			}
		}
		require.True(t, ok)

		for key := range m {
			if err == nil && strings.Contains(key, "/foo2") {
				ok = true
				break
			} else {
				ok = false
			}
		}
		require.True(t, ok)
	case "busybox:latest":
		_, ok = m[".wh.foo"]
		require.True(t, ok)

		_, ok = m["foo2/"]
		require.True(t, ok)

		_, ok = m["foo2/bar"]
		require.True(t, ok)
	}
}

func testRmSymlink(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// Test that if FileOp.Rm is called on a symlink, then
	// the symlink is removed rather than the target
	type testVars struct {
		imgName    string
		touchCmd   string
		symlinkCmd string
	}

	v := integration.UnixOrWindows(
		testVars{
			imgName:    "alpine",
			touchCmd:   "touch /mnt/target",
			symlinkCmd: "ln -s target /mnt/link",
		},
		testVars{
			imgName:    "nanoserver:latest",
			touchCmd:   "cmd /C type nul > /mnt/target",
			symlinkCmd: "cmd /c cd /d c:/mnt && mklink link target",
		},
	)

	mnt := llb.Image(v.imgName).
		Run(llb.Shlex(v.touchCmd)).
		AddMount("/mnt", llb.Scratch())

	mnt = llb.Image(v.imgName).
		Run(llb.Shlex(v.symlinkCmd)).
		AddMount("/mnt", mnt)

	def, err := mnt.File(llb.Rm("link")).Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.NoError(t, fstest.CheckDirectoryEqualWithApplier(destDir, fstest.CreateFile("target", nil, 0644)))
}

// #276
func testWhiteoutParentDir(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "mkdir -p foo; echo -n first > foo/bar;"`)
	run(`rm foo/bar`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	lastLayer := mfst.Layers[len(mfst.Layers)-1]

	layer, ok := m[ocispecs.ImageBlobsDir+"/sha256/"+lastLayer.Digest.Hex()]
	require.True(t, ok)

	m, err = testutil.ReadTarToMap(layer.Data, true)
	require.NoError(t, err)

	_, ok = m["foo/.wh.bar"]
	require.True(t, ok)

	_, ok = m["foo/"]
	require.True(t, ok)
}
