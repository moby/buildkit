package client

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"
)

func testBuildMultiMount(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgAlpine := integration.UnixOrWindows(
		"docker.io/library/alpine:latest",
		"nanoserver",
	)
	imgBusybox := integration.UnixOrWindows(
		"docker.io/library/busybox:latest",
		"nanoserver",
	)
	cmdStr1 := integration.UnixOrWindows(
		"/bin/ls -l",
		"cmd /C dir",
	)
	cmdStr2 := integration.UnixOrWindows(
		"/bin/cp -a /busybox/etc/passwd baz",
		`cmd /C copy C:\\busybox\\License.txt baz`,
	)

	alpine := llb.Image(imgAlpine)
	ls := alpine.Run(llb.Shlex(cmdStr1))
	busybox := llb.Image(imgBusybox)
	cp := ls.Run(llb.Shlex(cmdStr2))
	cp.AddMount("/busybox", busybox)

	def, err := cp.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testCachedMounts(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	scratch := func() llb.State {
		return integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))
	}

	busybox := llb.Image(imgName)
	// setup base for one of the cache sources
	cmdPrefix := integration.UnixOrWindows(
		`sh -c "echo -n`,
		`cmd /C "echo`,
	)
	st := busybox.Run(llb.Shlexf(`%s base > baz"`, cmdPrefix), llb.Dir("/wd"))
	base := st.AddMount("/wd", scratch())

	st = busybox.Run(llb.Shlexf(`%s first > foo"`, cmdPrefix), llb.Dir("/wd"))

	st.AddMount("/wd", scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))
	cmdStr := integration.UnixOrWindows(
		`sh -c "cat foo && echo -n second > /wd2/bar"`,
		`cmd /C type foo && echo second > C:\\wd2\\bar`,
	)
	st = st.Run(llb.Shlex(cmdStr), llb.Dir("/wd"))
	st.AddMount("/wd", scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))
	st.AddMount("/wd2", base, llb.AsPersistentCacheDir("mycache2", llb.CacheMountShared))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// repeat to make sure cache works
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// second build using cache directories
	cmdStr = integration.UnixOrWindows(
		`sh -c "cp /src0/foo . && cp /src1/bar . && cp /src1/baz ."`,
		`cmd /C copy C:\\src0\\foo . && copy C:\\src1\\bar . && copy C:\\src1\\baz .`,
	)
	st = busybox.Run(llb.Shlex(cmdStr), llb.Dir("/wd"))
	out := st.AddMount("/wd", scratch())
	st.AddMount("/src0", scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))
	st.AddMount("/src1", base, llb.AsPersistentCacheDir("mycache2", llb.CacheMountShared))

	destDir := t.TempDir()

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	newLine := integration.UnixOrWindows("", " \r\n")
	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "first"+newLine, string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "second"+newLine, string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "baz"))
	require.NoError(t, err)
	require.Equal(t, "base"+newLine, string(dt))

	checkAllReleasable(t, c, sb, true)
}

func testCacheMountNoCache(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")

	out := busybox.Run(llb.Shlex(`sh -e -c "touch /m1/foo; touch /m2/bar"`))
	out.AddMount("/m1", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))
	out.AddMount("/m2", llb.Scratch(), llb.AsPersistentCacheDir("mycache2", llb.CacheMountLocked))

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	out = busybox.Run(llb.Shlex(`sh -e -c "[[ ! -f /m1/foo ]]; touch /m1/foo2;"`), llb.IgnoreCache)
	out.AddMount("/m1", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	out = busybox.Run(llb.Shlex(`sh -e -c "[[ -f /m1/foo2 ]]; [[ -f /m2/bar ]];"`))
	out.AddMount("/m1", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))
	out.AddMount("/m2", llb.Scratch(), llb.AsPersistentCacheDir("mycache2", llb.CacheMountLocked))

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testDuplicateCacheMount(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	scratch := func() llb.State {
		return integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))
	}

	cmdStr := integration.UnixOrWindows(
		`sh -e -c "[[ ! -f /m2/foo ]]; touch /m1/foo; [[ -f /m2/foo ]];"`,
		`cmd /C echo a > \\m1\\foo && dir \\m2\\foo`,
	)
	out := busybox.Run(llb.Shlex(cmdStr))
	out.AddMount("/m1", scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))
	out.AddMount("/m2", scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testLayerLimitOnMounts(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	base := llb.Image("busybox:latest")

	const numLayers = 110

	for range numLayers {
		base = base.Run(llb.Shlex("sh -c 'echo hello >> /hello'")).Root()
	}

	def, err := base.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(ctx, def, SolveOpt{}, nil)
	require.NoError(t, err)

	ls := llb.Image("busybox:latest").
		Run(llb.Shlexf("ls -l /base/hello"))
	ls.AddMount("/base", base, llb.Readonly)

	def, err = ls.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(ctx, def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testLLBMountPerformance(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	srcPath := integration.UnixOrWindows("/bin", "/Windows")

	mntInput := llb.Image(imgName)
	st := llb.Image(imgName)
	var mnts []llb.State
	// Reduce iterations on Windows due to significantly slower container operations
	numIterations := integration.UnixOrWindows(20, 5)
	for range numIterations {
		execSt := st.Run(
			llb.Args(integration.UnixOrWindows(
				[]string{"true"},
				[]string{"cmd", "/C", "exit 0"},
			)),
		)
		mnts = append(mnts, mntInput)
		for j := range mnts {
			mnts[j] = execSt.AddMount(fmt.Sprintf("/tmp/bin%d", j), mnts[j], llb.SourcePath(srcPath))
		}
		st = execSt.Root()
	}

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	// Windows images take longer time, especially on CI systems
	// With reduced iterations (5 vs 20), use generous timeout
	timeout := integration.UnixOrWindows(time.Minute, 10*time.Minute)
	timeoutCtx, cancel := context.WithTimeoutCause(sb.Context(), timeout, nil)
	defer cancel()
	_, err = c.Solve(timeoutCtx, def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testLockedCacheMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.Run(llb.Shlex(`sh -e -c "touch one; if [[ -f two ]]; then exit 0; fi; for i in $(seq 10); do if [[ -f two ]]; then exit 1; fi; usleep 200000; done"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	st2 := busybox.Run(llb.Shlex(`sh -e -c "touch two; if [[ -f one ]]; then exit 0; fi; for i in $(seq 10); do if [[ -f one ]]; then exit 1; fi; usleep 200000; done"`), llb.Dir("/wd"))
	st2.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountLocked))

	out := busybox.Run(llb.Shlex("true"))
	out.AddMount("/m1", st.Root())
	out.AddMount("/m2", st2.Root())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

// testMountStubsDirectory verifies that BuildKit cleans up stub directories created for tmpfs
// mounts after a Run step completes. Empty stubs should be removed, stubs with user content
// should be kept, and pre-existing directories used as mount targets should be preserved.
func testMountStubsDirectory(t *testing.T, sb integration.Sandbox) {
	// Skipped on Windows because the test relies on tmpfs mounts, multiple simultaneous AddMount
	// calls, and llb.Scratch() — none of which are supported on Windows containers. The stub
	// cleanup behavior is also Linux-specific (tied to overlayfs mount point handling).
	integration.SkipOnPlatform(t, "windows", "requires tmpfs, multiple mounts, and Scratch — all unsupported on Windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		File(llb.Mkdir("/test", 0700)).
		File(llb.Mkdir("/test/qux/", 0700)).
		Run(
			llb.Args([]string{"touch", "/test/baz/keep"}),
			// check stubs directory is removed
			llb.AddMount("/test/foo", llb.Scratch(), llb.Tmpfs()),
			// check that stubs directory are recursively removed
			llb.AddMount("/test/bar/x/y", llb.Scratch(), llb.Tmpfs()),
			// check that only empty stubs directories are removed
			llb.AddMount("/test/baz/x", llb.Scratch(), llb.Tmpfs()),
			// check that previously existing directory are not removed
			llb.AddMount("/test/qux", llb.Scratch(), llb.Tmpfs()),
		).Root()
	st = llb.Scratch().File(llb.Copy(st, "/test", "/", &llb.CopyInfo{CopyDirContentsOnly: true}))
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	tmpDir := t.TempDir()
	tarFile := filepath.Join(tmpDir, "out.tar")
	tarFileW, err := os.Create(tarFile)
	require.NoError(t, err)
	defer tarFileW.Close()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(tarFileW),
			},
		},
	}, nil)
	require.NoError(t, err)
	tarFileW.Close()

	dt, err := os.ReadFile(tarFile)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	require.ElementsMatch(t, []string{
		"baz/",
		"baz/keep",
		"qux/",
	}, keys)
}

// testMountStubsTimestamp verifies that timestamps set on directories used as mount points
// (and their parents) are preserved after the mount is removed.
// https://github.com/moby/buildkit/issues/3148
func testMountStubsTimestamp(t *testing.T, sb integration.Sandbox) {
	// Skipped on Windows because the test relies on tmpfs mounts, multiple AddMount calls,
	// Scratch, and Linux-specific commands — all unsupported on Windows containers.
	integration.SkipOnPlatform(t, "windows", "requires tmpfs, multiple mounts, and Scratch — all unsupported on Windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	const sourceDateEpoch = int64(1234567890) // Fri Feb 13 11:31:30 PM UTC 2009
	st := llb.Image("busybox:latest").Run(
		llb.Args([]string{
			"/bin/touch", fmt.Sprintf("--date=@%d", sourceDateEpoch),
			"/bin",
			"/etc",
			"/var",
			"/var/foo",
			"/tmp",
			"/tmp/foo2",
			"/tmp/foo2/bar",
		}),
		llb.AddMount("/var/foo", llb.Scratch(), llb.Tmpfs()),
		llb.AddMount("/tmp/foo2/bar", llb.Scratch(), llb.Tmpfs()),
	)
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	tmpDir := t.TempDir()
	tarFile := filepath.Join(tmpDir, "out.tar")
	tarFileW, err := os.Create(tarFile)
	require.NoError(t, err)
	defer tarFileW.Close()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterTar,
				Output: fixedWriteCloser(tarFileW),
			},
		},
	}, nil)
	require.NoError(t, err)
	tarFileW.Close()

	tarFileR, err := os.Open(tarFile)
	require.NoError(t, err)
	defer tarFileR.Close()
	tarR := tar.NewReader(tarFileR)
	touched := map[string]*tar.Header{
		"bin/": nil, // Regular dir
		"etc/": nil, // Parent of file mounts (etc/{resolv.conf, hosts})
		"var/": nil, // Parent of dir mount (var/foo/)
		"tmp/": nil, // Grandparent of dir mount (tmp/foo2/bar/)
		// No support for reproducing the timestamps of mount point directories such as var/foo/ and tmp/foo2/bar/,
		// because the touched timestamp value is lost when the mount is unmounted.
	}
	for {
		hd, err := tarR.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if x, ok := touched[hd.Name]; ok && x == nil {
			touched[hd.Name] = hd
		}
	}
	for name, hd := range touched {
		t.Logf("Verifying %q (%+v)", name, hd)
		require.NotNil(t, hd, name)
		require.Equal(t, sourceDateEpoch, hd.ModTime.Unix(), name)
	}
}

// #319
func testMountWithNoSource(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))

	var nilState llb.State

	// This should never actually be run, but we want to succeed
	// if it was, because we expect an error below, or a daemon
	// panic if the issue has regressed.
	cmdArgs := integration.UnixOrWindows(
		[]string{"/bin/true"},
		[]string{"cmd", "/C", "exit 0"},
	)
	run := busybox.Run(
		llb.Args(cmdArgs),
		llb.AddMount("/nil", nilState, llb.SourcePath("/"), llb.Readonly))

	st = run.AddMount("/mnt", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	checkAllReleasable(t, c, sb, true)
}

func testRawSocketMount(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	listener := net.ListenConfig{}
	l, err := listener.Listen(context.TODO(), "unix", sockPath)
	require.NoError(t, err)
	defer l.Close()

	var called atomic.Bool
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called.Store(true)
			w.WriteHeader(http.StatusOK)
		}),
	}
	go srv.Serve(l)
	defer srv.Close()

	st := llb.Image("alpine:latest").
		Run(llb.Shlex(`apk add --no-cache curl`)).
		Run(llb.Shlex(`curl --unix-socket /tmp/test.sock http://./foo`),
			llb.AddSSHSocket(llb.SSHSocketTarget("/tmp/test.sock")),
		).Root()

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	raw, err := sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{
		Paths: []string{sockPath},
		Raw:   true,
	}})
	require.NoError(t, err)

	ch := make(chan *SolveStatus)
	go func() {
		for status := range ch {
			for _, l := range status.Logs {
				t.Log(string(l.Data))
			}
		}
	}()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{raw},
	}, ch)
	require.NoError(t, err)
	require.True(t, called.Load(), "server should have been called")
}

// #324
func testReadonlyRootFS(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("docker.io/library/busybox:latest")
	st := llb.Scratch()

	// The path /foo should be unwriteable.
	run := busybox.Run(
		llb.ReadonlyRootFS(),
		llb.Args([]string{"/bin/touch", "/foo"}))
	st = run.AddMount("/mnt", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	// Would prefer to detect more specifically "Read-only file
	// system" but that isn't exposed here (it is on the stdio
	// which we don't see).
	require.Contains(t, err.Error(), "process \"/bin/touch /foo\" did not complete successfully")

	checkAllReleasable(t, c, sb, true)
}

func testRunCacheWithMounts(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)

	imgAlpine := integration.UnixOrWindows("alpine:latest", "nanoserver:plus")
	alpineImage := llb.Image(imgAlpine)

	// On Windows ARM64, nanoserver:plus and nanoserver:latest map to the same
	// image, so we create a marker file to distinguish the mounted image.
	mountSource := alpineImage
	if runtime.GOOS == "windows" {
		mountSource = alpineImage.Run(
			llb.Shlex(`cmd /C echo 1> C:/marker`),
		).Root()
	}

	cmdStr := integration.UnixOrWindows(
		`sh -e -c "[[ -f /m1/sbin/apk ]]"`,
		`cmd /C if exist C:/m1/marker (exit 0) else (exit 1)`,
	)
	out := busybox.Run(llb.Shlex(cmdStr))
	out.AddMount("/m1", mountSource, llb.Readonly)

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	cmdStr = integration.UnixOrWindows(
		`sh -e -c "[[ ! -f /m1/sbin/apk ]]"`,
		`cmd /C if exist C:/m1/marker (exit 1) else (exit 0)`,
	)
	out = busybox.Run(llb.Shlex(cmdStr))
	out.AddMount("/m1", llb.Image(imgName), llb.Readonly)

	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

/*
testSecretEnv verifies that secrets can be injected as environment variables during a build.
It tests four scenarios: (1) a provided secret is accessible via env var, (2) an optional
secret that is not provided results in an unset/empty env var, (3) a required secret that
is not provided causes an error, and (4) multiple secrets with custom IDs are resolved correctly.
Works on both Linux (busybox) and Windows (nanoserver).
*/
func testSecretEnv(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	var st llb.ExecState

	// Test 1: Verify secret value is accessible as environment variable
	switch imgName {
	case "nanoserver:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`cmd /C "if "%MY_SECRET%"=="foo-secret" (exit 0) else (exit 1)"`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true)))
	case "busybox:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`sh -c '[ "$(echo ${MY_SECRET})" = 'foo-secret' ]'`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true)))
	}

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"MY_SECRET": []byte("foo-secret"),
		})},
	}, nil)
	require.NoError(t, err)

	// Test 2: Optional secret not provided should be unset/empty
	switch imgName {
	case "nanoserver:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`cmd /C "if not defined MY_SECRET (exit 0) else (exit 1)"`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true), llb.SecretOptional))
	case "busybox:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`sh -c '[ -z "${MY_SECRET}" ]'`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true), llb.SecretOptional))
	}

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// Test 3: Required secret not provided should error
	switch imgName {
	case "nanoserver:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`cmd /C "echo foo"`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true)))
	case "busybox:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`echo foo`), llb.AddSecret("MY_SECRET", llb.SecretAsEnv(true)))
	}

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.Error(t, err)

	// Test 4: Multiple secrets with custom IDs
	switch imgName {
	case "nanoserver:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`cmd /C "if "%MYPASSWORD%-%MYTOKEN%"=="pw-token" (exit 0) else (exit 1)"`),
				llb.AddSecret("MYPASSWORD", llb.SecretID("pass"), llb.SecretAsEnv(true)),
				llb.AddSecret("MYTOKEN", llb.SecretAsEnv(true)),
			)
	case "busybox:latest":
		st = llb.Image(imgName).
			Run(llb.Shlex(`sh -c '[ "$(echo ${MYPASSWORD}-${MYTOKEN})" = "pw-token" ]' `),
				llb.AddSecret("MYPASSWORD", llb.SecretID("pass"), llb.SecretAsEnv(true)),
				llb.AddSecret("MYTOKEN", llb.SecretAsEnv(true)),
			)
	}

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"pass":    []byte("pw"),
			"MYTOKEN": []byte("token"),
		})},
	}, nil)
	require.NoError(t, err)
}

/*
testSecretMounts verifies file-based secret mounts: content readability, optional/required
behavior, custom permissions, and empty secrets. Skipped on Windows because BuildKit uses
tmpfs for secret mounts, which Windows does not support. See testSecretEnv for a
cross-platform alternative using environment-based secrets.
*/
func testSecretMounts(t *testing.T, sb integration.Sandbox) {
	// Windows vs Linux secret implementation differences:
	//
	// Linux: Secrets are mounted as files using tmpfs at /run/secrets/ (RAM-based, encrypted)
	//
	// Windows: Secrets are stored in C:\ProgramData\Docker\internal\secrets (clear text on disk)
	//          Symbolic links point to the desired target (default: C:\ProgramData\Docker\secrets)
	//          Windows does NOT support tmpfs or non-directory file bind-mounts
	//          UID/GID/mode options are NOT supported on Windows
	//          Recommend BitLocker for at-rest encryption
	//
	// BuildKit Issue: The "invalid windows mount type: 'tmpfs'" error occurs because BuildKit
	// currently tries to use tmpfs for all secret mounts. This needs to be fixed in BuildKit's
	// secret mount implementation to use the Windows symlink approach instead.

	// For now, this test is Linux-only until BuildKit properly implements Windows secret mounts
	// without tmpfs. Use testSecretEnv for Windows-compatible environment-based secrets.
	integration.SkipOnPlatform(t, "windows", "Windows does not support tmpfs for secret mounts")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// Test 1: Basic secret mount with content verification (Linux only)
	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c 'mount | grep mysecret | grep "type tmpfs" && [ "$(cat /run/secrets/mysecret)" = 'foo-secret' ]'`), llb.AddSecret("/run/secrets/mysecret"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"/run/secrets/mysecret": []byte("foo-secret"),
		})},
	}, nil)
	require.NoError(t, err)

	// Test 2: Optional secret - mount should not exist when secret not present in SolveOpt
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`test ! -f /run/secrets/mysecret2`), llb.AddSecret("/run/secrets/mysecret2", llb.SecretOptional))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// Test 3: Required secret missing - should error
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`echo secret3`), llb.AddSecret("/run/secrets/mysecret3"))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{})},
	}, nil)
	require.Error(t, err)

	// Test 4: Secret with custom ID and file permissions
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c '[ "$(stat -c "%u %g %f" /run/secrets/mysecret4)" = "1 1 81ff" ]' `), llb.AddSecret("/run/secrets/mysecret4", llb.SecretID("mysecret"), llb.SecretFileOpt(1, 1, 0777)))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"mysecret": []byte("pw"),
		})},
	}, nil)
	require.NoError(t, err)

	// Test 5: Empty secret still creates secret file
	st = llb.Image("busybox:latest").
		Run(llb.Shlex(`test -f /run/secrets/mysecret5`), llb.AddSecret("/run/secrets/mysecret5", llb.SecretID("mysecret")))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{secretsprovider.FromMap(map[string][]byte{
			"mysecret": []byte(""),
		})},
	}, nil)
	require.NoError(t, err)
}

func testSharedCacheMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.Run(llb.Shlex(`sh -e -c "touch one; while [[ ! -f two ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	st2 := busybox.Run(llb.Shlex(`sh -e -c "touch two; while [[ ! -f one ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st2.AddMount("/wd", llb.Scratch(), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	out := busybox.Run(llb.Shlex("true"))
	out.AddMount("/m1", st.Root())
	out.AddMount("/m2", st2.Root())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

// #2334
func testSharedCacheMountsNoScratch(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.Run(llb.Shlex(`sh -e -c "touch one; while [[ ! -f two ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st.AddMount("/wd", llb.Image("busybox:latest"), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	st2 := busybox.Run(llb.Shlex(`sh -e -c "touch two; while [[ ! -f one ]]; do ls -l; usleep 500000; done"`), llb.Dir("/wd"))
	st2.AddMount("/wd", llb.Image("busybox:latest"), llb.AsPersistentCacheDir("mycache1", llb.CacheMountShared))

	out := busybox.Run(llb.Shlex("true"))
	out.AddMount("/m1", st.Root())
	out.AddMount("/m2", st2.Root())

	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testSSHMount(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	a := agent.NewKeyring()

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	err = a.Add(agent.AddedKey{PrivateKey: k})
	require.NoError(t, err)

	sockPath, err := makeSSHAgentSock(t, a)
	require.NoError(t, err)

	ssh, err := sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{
		Paths: []string{sockPath},
	}})
	require.NoError(t, err)

	// no ssh exposed
	st := llb.Image("busybox:latest").Run(llb.Shlex(`nosuchcmd`), llb.AddSSHSocket())
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no SSH key ")

	// custom ID not exposed
	st = llb.Image("busybox:latest").Run(llb.Shlex(`nosuchcmd`), llb.AddSSHSocket(llb.SSHID("customID")))
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{ssh},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unset ssh forward key customID")

	// missing custom ID ignored on optional
	st = llb.Image("busybox:latest").Run(llb.Shlex(`ls`), llb.AddSSHSocket(llb.SSHID("customID"), llb.SSHOptional))
	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	// valid socket
	st = llb.Image("alpine:latest").
		Run(llb.Shlex(`apk add --no-cache openssh`)).
		Run(llb.Shlex(`sh -c 'echo -n $SSH_AUTH_SOCK > /out/sock && ssh-add -l > /out/out'`),
			llb.AddSSHSocket())

	out := st.AddMount("/out", llb.Scratch())
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "sock"))
	require.NoError(t, err)
	require.Equal(t, "/run/buildkit/ssh_agent.0", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "2048")
	require.Contains(t, string(dt), "(RSA)")

	// forbidden command
	st = llb.Image("alpine:latest").
		Run(llb.Shlex(`apk add --no-cache openssh`)).
		Run(llb.Shlex(`sh -c 'ssh-keygen -f /tmp/key -N "" && ssh-add -k /tmp/key 2> /out/out || true'`),
			llb.AddSSHSocket())

	out = st.AddMount("/out", llb.Scratch())
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "agent refused operation")

	// valid socket from key on disk
	st = llb.Image("alpine:latest").
		Run(llb.Shlex(`apk add --no-cache openssh`)).
		Run(llb.Shlex(`sh -c 'ssh-add -l > /out/out'`),
			llb.AddSSHSocket())

	out = st.AddMount("/out", llb.Scratch())
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)

	k, err = rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	dt = pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(k),
		},
	)

	tmpDir := t.TempDir()

	err = os.WriteFile(filepath.Join(tmpDir, "key"), dt, 0600)
	require.NoError(t, err)

	ssh, err = sshprovider.NewSSHAgentProvider([]sshprovider.AgentConfig{{
		Paths: []string{filepath.Join(tmpDir, "key")},
	}})
	require.NoError(t, err)

	destDir = t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		Session: []session.Attachable{ssh},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "2048")
	require.Contains(t, string(dt), "(RSA)")
}

func testTmpfsMounts(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c 'mount | grep /foobar | grep "type tmpfs" && touch /foobar/test'`), llb.AddMount("/foobar", llb.Scratch(), llb.Tmpfs()))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func cacheMountFile(base llb.State, cacheID, name string) llb.State {
	check := base.Run(llb.Shlexf(`sh -e -c "echo %s >/dev/null; cp /cache/%s /out/%s"`, cacheID, name, name))
	check.AddMount("/cache", llb.Scratch(), llb.AsPersistentCacheDir(cacheID, llb.CacheMountShared))
	return check.AddMount("/out", llb.Scratch())
}

func makeSSHAgentSock(t *testing.T, agent agent.Agent) (p string, err error) {
	tmpDir := integration.Tmpdir(t)
	sockPath := filepath.Join(tmpDir.Name, "ssh_auth_sock")

	listener := net.ListenConfig{}
	l, err := listener.Listen(context.TODO(), "unix", sockPath)
	if err != nil {
		return "", err
	}
	t.Cleanup(func() {
		require.NoError(t, l.Close())
	})

	s := &server{l: l}
	go s.run(agent)

	return sockPath, nil
}

type server struct {
	l net.Listener
}

func (s *server) run(a agent.Agent) error {
	for {
		c, err := s.l.Accept()
		if err != nil {
			return err
		}

		go agent.ServeAgent(a, c)
	}
}
