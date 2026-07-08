package dockerfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testShmSize(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	dockerfile := []byte(`
FROM busybox AS base
RUN mount | grep /dev/shm > /shmsize
FROM scratch
COPY --from=base /shmsize /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"shm-size": "134217728",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "shmsize"))
	require.NoError(t, err)
	require.Contains(t, string(dt), `size=131072k`)
}

func testUlimit(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	dockerfile := []byte(`
FROM busybox AS base
RUN ulimit -n > /ulimit
FROM scratch
COPY --from=base /ulimit /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"ulimit": "nofile=1062:1062",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "ulimit"))
	require.NoError(t, err)
	require.Equal(t, `1062`, strings.TrimSpace(string(dt)))
}

func testCgroupParent(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	if sb.Rootless() {
		t.SkipNow()
	}

	if _, err := os.Lstat("/sys/fs/cgroup/cgroup.subtree_control"); os.IsNotExist(err) {
		t.Skipf("test requires cgroup v2")
	}

	cgroupName := "test." + identity.NewID()

	err := os.MkdirAll(filepath.Join("/sys/fs/cgroup", cgroupName), 0755)
	require.NoError(t, err)

	defer func() {
		err := os.RemoveAll(filepath.Join("/sys/fs/cgroup", cgroupName))
		require.NoError(t, err)
	}()

	err = os.WriteFile(filepath.Join("/sys/fs/cgroup", cgroupName, "pids.max"), []byte("10"), 0644)
	require.NoError(t, err)

	f := getFrontend(t, sb)
	dockerfile := []byte(`
FROM alpine AS base
RUN mkdir /out; (for i in $(seq 1 10); do sleep 1 & done 2>/out/error); cat /proc/self/cgroup > /out/cgroup
FROM scratch
COPY --from=base /out /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"cgroup-parent": cgroupName,
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "cgroup"))
	require.NoError(t, err)
	// cgroupns does not leak the parent cgroup name
	require.NotContains(t, strings.TrimSpace(string(dt)), `foocgroup`)

	dt, err = os.ReadFile(filepath.Join(destDir, "error"))
	require.NoError(t, err)
	require.Contains(t, strings.TrimSpace(string(dt)), `Resource temporarily unavailable`)
}

func testLinuxResources(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	if sb.Rootless() {
		t.SkipNow()
	}

	if _, err := os.Lstat("/sys/fs/cgroup/cgroup.subtree_control"); os.IsNotExist(err) {
		t.Skipf("test requires cgroup v2")
	}

	f := getFrontend(t, sb)
	dockerfile := []byte(`
FROM alpine AS base
RUN mkdir /out && cat /sys/fs/cgroup/memory.max > /out/memory
FROM scratch
COPY --from=base /out /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"memory": "67108864",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "memory"))
	require.NoError(t, err)
	require.Equal(t, "67108864", strings.TrimSpace(string(dt)))
}
