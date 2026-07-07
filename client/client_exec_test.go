package client

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testCgroupParent(t *testing.T, sb integration.Sandbox) {
	if sb.Rootless() {
		t.SkipNow()
	}

	if _, err := os.Lstat("/sys/fs/cgroup/cgroup.subtree_control"); os.IsNotExist(err) {
		t.Skipf("test requires cgroup v2")
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	img := llb.Image("alpine:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = img.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	cgroupName := "test." + identity.NewID()

	err = os.MkdirAll(filepath.Join("/sys/fs/cgroup", cgroupName), 0755)
	require.NoError(t, err)

	defer func() {
		err := os.RemoveAll(filepath.Join("/sys/fs/cgroup", cgroupName))
		require.NoError(t, err)
	}()

	err = os.WriteFile(filepath.Join("/sys/fs/cgroup", cgroupName, "pids.max"), []byte("10"), 0644)
	require.NoError(t, err)

	run(`sh -c "(for i in $(seq 1 10); do sleep 1 & done 2>first.error); cat /proc/self/cgroup >> first"`, llb.WithCgroupParent(cgroupName))
	run(`sh -c "(for i in $(seq 1 10); do sleep 1 & done 2>second.error); cat /proc/self/cgroup >> second"`)

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

	// neither process leaks parent cgroup name inside container
	dt, err := os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	require.NotContains(t, strings.TrimSpace(string(dt)), cgroupName)

	dt2, err := os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	require.NotContains(t, strings.TrimSpace(string(dt2)), cgroupName)

	dt, err = os.ReadFile(filepath.Join(destDir, "first.error"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), "Resource temporarily unavailable")

	dt, err = os.ReadFile(filepath.Join(destDir, "second.error"))
	require.NoError(t, err)
	require.Equal(t, "", strings.TrimSpace(string(dt)))
}

func testLinuxResources(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	if sb.Rootless() {
		t.SkipNow()
	}

	if _, err := os.Lstat("/sys/fs/cgroup/cgroup.subtree_control"); os.IsNotExist(err) {
		t.Skipf("test requires cgroup v2")
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	img := llb.Image("alpine:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = img.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	// Test memory limit: set 64MiB and verify via cgroup
	run(`sh -c "cat /sys/fs/cgroup/memory.max > mem_limited"`, llb.MemoryLimit(64*1024*1024))
	run(`sh -c "cat /sys/fs/cgroup/memory.max > mem_default"`)

	// Test CPU quota: set quota=50000 period=100000 (50% CPU) and verify
	run(`sh -c "cat /sys/fs/cgroup/cpu.max > cpu_limited"`, llb.CPUQuota(50000), llb.CPUPeriod(100000))

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

	dt, err := os.ReadFile(filepath.Join(destDir, "mem_limited"))
	require.NoError(t, err)
	require.Equal(t, "67108864", strings.TrimSpace(string(dt)))

	dt2, err := os.ReadFile(filepath.Join(destDir, "mem_default"))
	require.NoError(t, err)
	require.Equal(t, "max", strings.TrimSpace(string(dt2)))

	dt3, err := os.ReadFile(filepath.Join(destDir, "cpu_limited"))
	require.NoError(t, err)
	require.Equal(t, "50000 100000", strings.TrimSpace(string(dt3)))
}

func testPassthroughOp(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	ctx := sb.Context()
	busybox := llb.Image("busybox:latest")
	base := llb.Scratch().File(llb.Mkfile("/base", 0644, []byte("base")))

	t.Run("requires returns receiver and builds non-output dependency", func(t *testing.T) {
		cacheID := "passthrough-requires-" + identity.NewID()
		nonOutputDep := busybox.Run(llb.Shlex(`sh -e -c "echo requires-dep-built > /cache/marker; echo should-not-export > /not-output"`))
		nonOutputDep.AddMount("/cache", llb.Scratch(), llb.AsPersistentCacheDir(cacheID, llb.CacheMountShared))

		outDir := solveStateToLocalDir(ctx, t, c, base.Requires("test.passthrough.requires", nonOutputDep.Root()))
		requireLocalFile(t, outDir, "base", "base")
		require.NoFileExists(t, filepath.Join(outDir, "not-output"))

		markerDir := solveStateToLocalDir(ctx, t, c, cacheMountFile(busybox, cacheID, "marker"))
		requireLocalFile(t, markerDir, "marker", "requires-dep-built\n")
	})

	t.Run("direct passthrough returns selected outputs and builds non-output input", func(t *testing.T) {
		cacheID := "passthrough-direct-" + identity.NewID()
		nonOutputInput := busybox.Run(llb.Shlex(`sh -e -c "echo direct-input-built > /cache/marker; echo should-not-export > /not-output"`))
		nonOutputInput.AddMount("/cache", llb.Scratch(), llb.AsPersistentCacheDir(cacheID, llb.CacheMountShared))

		pass := llb.NewPassthroughOp("test.passthrough.direct", []llb.PassthroughInput{
			{State: llb.Scratch().File(llb.Mkfile("/a", 0644, []byte("a"))), Output: true},
			{State: nonOutputInput.Root()},
			{State: llb.Scratch().File(llb.Mkfile("/c", 0644, []byte("c"))), Output: true},
		})

		outA := llb.NewState(pass.Output())
		outC := llb.NewState(pass.OutputAt(1))
		out := llb.Scratch().
			File(llb.Copy(outA, "/a", "/a")).
			File(llb.Copy(outC, "/c", "/c"))

		outDir := solveStateToLocalDir(ctx, t, c, out)
		requireLocalFile(t, outDir, "a", "a")
		requireLocalFile(t, outDir, "c", "c")
		require.NoFileExists(t, filepath.Join(outDir, "not-output"))

		markerDir := solveStateToLocalDir(ctx, t, c, cacheMountFile(busybox, cacheID, "marker"))
		requireLocalFile(t, markerDir, "marker", "direct-input-built\n")
	})

	t.Run("failing non-output dependency fails solve", func(t *testing.T) {
		fail := busybox.Run(llb.Shlex(`sh -e -c "exit 42"`)).Root()
		def, err := base.Requires("test.passthrough.fail", fail).Marshal(ctx)
		require.NoError(t, err)
		_, err = c.Solve(ctx, def, SolveOpt{
			Exports: []ExportEntry{{
				Type:      ExporterLocal,
				OutputDir: t.TempDir(),
			}},
		}, nil)
		require.Error(t, err)
	})
}

// testRelativeMountpoint is a test that relative paths for mountpoints don't
// fail when runc is upgraded to at least rc95, which introduces an error when
// mountpoints are not absolute. Relative paths should be transformed to
// absolute points based on the llb.State's current working directory.
func testRelativeMountpoint(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	id := identity.NewID()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	scratch := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))
	cmdStr := integration.UnixOrWindows(
		"sh -c 'echo -n %s > /root/relpath/data'",
		`cmd /C echo %s > \\root\\relpath\\data`,
	)
	st := llb.Image(imgName).Dir("/root").Run(
		llb.Shlexf(cmdStr, id),
	).AddMount("relpath", scratch)

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

	dt, err := os.ReadFile(filepath.Join(destDir, "data"))
	require.NoError(t, err)
	newLine := integration.UnixOrWindows("", " \r\n")
	require.Equal(t, dt, []byte(id+newLine))
}

func testRelativeWorkDir(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows(
		"docker.io/library/busybox:latest",
		"mcr.microsoft.com/windows/nanoserver:ltsc2022",
	)
	cmdStr := integration.UnixOrWindows(
		`sh -c "pwd > /out/pwd"`,
		`cmd /C "cd > /out/pwd"`,
	)
	pwd := llb.Image(imgName).
		Dir("test1").
		Dir("test2").
		Run(llb.Shlex(cmdStr)).
		AddMount("/out", llb.Scratch())

	def, err := pwd.Marshal(sb.Context())
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

	dt, err := os.ReadFile(filepath.Join(destDir, "pwd"))
	require.NoError(t, err)
	pathStr := integration.UnixOrWindows(
		"/test1/test2\n",
		"C:\\test1\\test2\r\n",
	)
	require.Equal(t, []byte(pathStr), dt)
}

func testRunValidExitCodes(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// no exit codes specified, equivalent to [0]
	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	out := llb.Image(imgName)
	shellPrefix := integration.UnixOrWindows("sh -c", "cmd /C")

	out = out.Run(llb.Shlexf(`%s "exit 0"`, shellPrefix)).Root()
	out = out.Run(llb.Shlexf(`%s "exit 1"`, shellPrefix)).Root()
	def, err := out.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "exit code: 1")

	// empty exit codes, equivalent to [0]
	out = llb.Image(imgName)
	out = out.Run(llb.Shlexf(`%s "exit 0"`, shellPrefix), llb.ValidExitCodes()).Root()
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// if we expect non-zero, those non-zero codes should succeed
	out = llb.Image(imgName)
	out = out.Run(llb.Shlexf(`%s "exit 1"`, shellPrefix), llb.ValidExitCodes(1)).Root()
	out = out.Run(llb.Shlexf(`%s "exit 2"`, shellPrefix), llb.ValidExitCodes(2, 3)).Root()
	out = out.Run(llb.Shlexf(`%s "exit 3"`, shellPrefix), llb.ValidExitCodes(2, 3)).Root()
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	// if we expect non-zero, returning zero should fail
	out = llb.Image(imgName)
	out = out.Run(llb.Shlexf(`%s "exit 0"`, shellPrefix), llb.ValidExitCodes(1)).Root()
	def, err = out.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "exit code: 0")
}

func testSecurityMode(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureSecurityMode)
	command := `sh -c 'cat /proc/self/status | grep CapEff | cut -f 2 > /out'`
	mode := llb.SecurityModeSandbox
	var allowedEntitlements []string
	var assertCaps func(caps uint64)
	secMode := sb.Value("secmode")
	if secMode == securitySandbox {
		assertCaps = func(caps uint64) {
			/*
				$ capsh --decode=00000000a80425fb
				0x00000000a80425fb=cap_chown,cap_dac_override,cap_fowner,cap_fsetid,cap_kill,cap_setgid,cap_setuid,cap_setpcap,
				cap_net_bind_service,cap_net_raw,cap_sys_chroot,cap_mknod,cap_audit_write,cap_setfcap
			*/
			require.Equal(t, uint64(0xa80425fb), caps)
		}
		allowedEntitlements = []string{}
	} else {
		assertCaps = func(caps uint64) {
			/*
				$ capsh --decode=0000003fffffffff
				0x0000003fffffffff=cap_chown,cap_dac_override,cap_dac_read_search,cap_fowner,cap_fsetid,cap_kill,cap_setgid,
				cap_setuid,cap_setpcap,cap_linux_immutable,cap_net_bind_service,cap_net_broadcast,cap_net_admin,cap_net_raw,
				cap_ipc_lock,cap_ipc_owner,cap_sys_module,cap_sys_rawio,cap_sys_chroot,cap_sys_ptrace,cap_sys_pacct,cap_sys_admin,
				cap_sys_boot,cap_sys_nice,cap_sys_resource,cap_sys_time,cap_sys_tty_config,cap_mknod,cap_lease,cap_audit_write,
				cap_audit_control,cap_setfcap,cap_mac_override,cap_mac_admin,cap_syslog,cap_wake_alarm,cap_block_suspend,cap_audit_read
			*/

			// require that _at least_ minimum capabilities are granted
			require.Equal(t, uint64(0x3fffffffff), caps&0x3fffffffff)
		}
		mode = llb.SecurityModeInsecure
		allowedEntitlements = []string{entitlements.EntitlementSecurityInsecure.String()}
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").
		Run(llb.Shlex(command),
			llb.Security(mode))

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
		AllowedEntitlements: allowedEntitlements,
	}, nil)

	require.NoError(t, err)

	contents, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)

	caps, err := strconv.ParseUint(strings.TrimSpace(string(contents)), 16, 64)
	require.NoError(t, err)

	t.Logf("Caps: %x", caps)

	assertCaps(caps)
}

func testSecurityModeErrors(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()
	secMode := sb.Value("secmode")
	if secMode == securitySandbox {
		st := llb.Image("busybox:latest").
			Run(llb.Shlex(`sh -c 'echo sandbox'`))

		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{
			AllowedEntitlements: []string{entitlements.EntitlementSecurityInsecure.String()},
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "security.insecure is not allowed")
	}
	if secMode == securityInsecure {
		st := llb.Image("busybox:latest").
			Run(llb.Shlex(`sh -c 'echo insecure'`), llb.Security(llb.SecurityModeInsecure))

		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "security.insecure is not allowed")
	}

	st := llb.Image("busybox:latest").
		Run(llb.Shlex("echo invalid"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	var foundExec bool
	digestMap := map[digest.Digest]digest.Digest{}
	for i, dt := range def.Def {
		oldDigest := digest.FromBytes(dt)
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(dt))
		changed := false

		for j, input := range op.Inputs {
			if newDigest, ok := digestMap[digest.Digest(input.Digest)]; ok {
				op.Inputs[j].Digest = string(newDigest)
				changed = true
			}
		}

		if execOp := op.GetExec(); execOp != nil {
			execOp.Security = pb.SecurityMode(2)
			changed = true
			foundExec = true
		}

		if changed {
			newDt, err := op.MarshalVT()
			require.NoError(t, err)
			def.Def[i] = newDt

			newDigest := digest.FromBytes(newDt)
			digestMap[oldDigest] = newDigest
			if meta, ok := def.Metadata[oldDigest]; ok {
				delete(def.Metadata, oldDigest)
				def.Metadata[newDigest] = meta
			}
		}
	}
	require.True(t, foundExec)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid security mode")
}

func testSecurityModeSysfs(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureSecurityMode)
	if sb.Rootless() {
		t.SkipNow()
	}

	mode := llb.SecurityModeSandbox
	var allowedEntitlements []string
	secMode := sb.Value("secmode")
	if secMode == securitySandbox {
		allowedEntitlements = []string{}
	} else {
		mode = llb.SecurityModeInsecure
		allowedEntitlements = []string{entitlements.EntitlementSecurityInsecure.String()}
	}

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	cg := "/sys/fs/cgroup/cpuset/securitytest" // cgroup v1
	if _, err := os.Stat("/sys/fs/cgroup/cpuset"); errors.Is(err, os.ErrNotExist) {
		cg = "/sys/fs/cgroup/securitytest" // cgroup v2
	}

	command := "mkdir " + cg
	st := llb.Image("busybox:latest").
		Run(llb.Shlex(command),
			llb.Security(mode))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		AllowedEntitlements: allowedEntitlements,
	}, nil)

	if secMode == securitySandbox {
		require.Error(t, err)
		require.Contains(t, err.Error(), "did not complete successfully")
		require.Contains(t, err.Error(), "mkdir "+cg)
	} else {
		require.NoError(t, err)
	}
}

func testShmSize(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").Run(
		llb.AddMount("/dev/shm", llb.Scratch(), llb.Tmpfs(llb.TmpfsSize(128*1024*1024))),
		llb.Shlex(`sh -c 'mount | grep /dev/shm > /out/out'`),
	)

	out := st.AddMount("/out", llb.Scratch())
	def, err := out.Marshal(sb.Context())
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

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Contains(t, string(dt), `size=131072k`)
}

// moby/buildkit#614
func testStdinClosed(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	cmdStr := integration.UnixOrWindows("cat", "cmd /C more")
	st := llb.Image(imgName).Run(llb.Shlex(cmdStr))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testUlimit(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string, ro ...llb.RunOption) {
		st = busybox.Run(append(ro, llb.Shlex(cmd), llb.Dir("/wd"))...).AddMount("/wd", st)
	}

	run(`sh -c "ulimit -n > first"`, llb.AddUlimit(llb.UlimitNofile, 1062, 1062))
	run(`sh -c "ulimit -n > second"`)

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

	dt, err := os.ReadFile(filepath.Join(destDir, "first"))
	require.NoError(t, err)
	require.Equal(t, `1062`, strings.TrimSpace(string(dt)))

	dt2, err := os.ReadFile(filepath.Join(destDir, "second"))
	require.NoError(t, err)
	require.NotEqual(t, `1062`, strings.TrimSpace(string(dt2)))
}

func testUser(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	st := llb.Image("busybox:latest").Run(llb.Shlex(`sh -c "mkdir -m 0777 /wd"`))

	run := func(user, cmd string) {
		if user != "" {
			st = st.Run(llb.Shlex(cmd), llb.Dir("/wd"), llb.User(user))
		} else {
			st = st.Run(llb.Shlex(cmd), llb.Dir("/wd"))
		}
	}

	run("daemon", `sh -c "id -nu > user"`)
	run("daemon:daemon", `sh -c "id -ng > group"`)
	run("daemon:nobody", `sh -c "id -ng > nobody"`)
	run("1:1", `sh -c "id -g > userone"`)
	run("root", `sh -c "id -Gn > root_supplementary"`)
	run("", `sh -c "id -Gn > default_supplementary"`)
	run("", `rm /etc/passwd /etc/group`) // test that default user still works
	run("", `sh -c "id -u > default_uid"`)

	st = st.Run(llb.Shlex("cp -a /wd/. /out/"))
	out := st.AddMount("/out", llb.Scratch())

	def, err := out.Marshal(sb.Context())
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

	dt, err := os.ReadFile(filepath.Join(destDir, "user"))
	require.NoError(t, err)
	require.Equal(t, "daemon", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "group"))
	require.NoError(t, err)
	require.Equal(t, "daemon", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "nobody"))
	require.NoError(t, err)
	require.Equal(t, "nobody", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "userone"))
	require.NoError(t, err)
	require.Equal(t, "1", strings.TrimSpace(string(dt)))

	dt, err = os.ReadFile(filepath.Join(destDir, "root_supplementary"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(dt), "root "))
	require.Contains(t, string(dt), "wheel")

	dt2, err := os.ReadFile(filepath.Join(destDir, "default_supplementary"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))

	dt, err = os.ReadFile(filepath.Join(destDir, "default_uid"))
	require.NoError(t, err)
	require.Equal(t, "0", strings.TrimSpace(string(dt)))

	checkAllReleasable(t, c, sb, true)
}

func requireLocalFile(t *testing.T, dir, name, expected string) {
	t.Helper()

	dt, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	require.Equal(t, expected, string(dt))
}

func solveStateToLocalDir(ctx context.Context, t *testing.T, c *Client, st llb.State) string {
	t.Helper()

	def, err := st.Marshal(ctx)
	require.NoError(t, err)

	outDir := t.TempDir()
	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{{
			Type:      ExporterLocal,
			OutputDir: outDir,
		}},
	}, nil)
	require.NoError(t, err)
	return outDir
}

type secModeSandbox struct{}

func (*secModeSandbox) UpdateConfigFile(in string) (string, func() error) {
	return in, nil
}

type secModeInsecure struct{}

func (*secModeInsecure) UpdateConfigFile(in string) (string, func() error) {
	return in + "\n\ninsecure-entitlements = [\"security.insecure\"]\n", nil
}

var (
	securitySandbox  integration.ConfigUpdater = &secModeSandbox{}
	securityInsecure integration.ConfigUpdater = &secModeInsecure{}
)
