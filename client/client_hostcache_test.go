package client

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
)

const hostCacheTestID = "hostcache-test"

// hostCacheMounts gives every sandbox daemon its own host directory for
// hostCacheTestID, so parallel tests and workers never share cache contents.
type hostCacheMounts struct{}

func (*hostCacheMounts) UpdateConfigFile(in string) (string, func() error) {
	dir, err := os.MkdirTemp("", "buildkit-hostcache")
	if err != nil {
		panic(err)
	}
	// Rootless daemons run as an unprivileged user and create the directory.
	if err := os.Chmod(dir, 0o777); err != nil {
		panic(err)
	}
	return fmt.Sprintf(`%s

[cacheMounts.%q]
  path = %q
`, in, hostCacheTestID, filepath.Join(dir, "cache")), func() error {
			return os.RemoveAll(dir)
		}
}

func requireHostCacheMounts(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureHostCacheMounts)
}

func testHostCacheMountSurvivesPrune(t *testing.T, sb integration.Sandbox) {
	requireHostCacheMounts(t, sb)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")

	run := busybox.Run(llb.Shlex(`sh -e -c "echo host > /host/file; echo snap > /snap/file"`))
	run.AddMount("/host", llb.Scratch(), llb.AsPersistentCacheDir(hostCacheTestID, llb.CacheMountLocked))
	run.AddMount("/snap", llb.Scratch(), llb.AsPersistentCacheDir("snapshot-test", llb.CacheMountLocked))
	def, err := run.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	du, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)
	for _, r := range du {
		require.NotContains(t, r.Description, hostCacheTestID, "host cache mounts are not BuildKit cache records")
	}

	require.NoError(t, c.Prune(sb.Context(), nil, PruneAll))

	run = busybox.Run(llb.Shlex(`sh -e -c "[ \"$(cat /host/file)\" = host ]; [ ! -e /snap/file ]"`))
	run.AddMount("/host", llb.Scratch(), llb.AsPersistentCacheDir(hostCacheTestID, llb.CacheMountLocked))
	run.AddMount("/snap", llb.Scratch(), llb.AsPersistentCacheDir("snapshot-test", llb.CacheMountLocked))
	def, err = run.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

// solveWithHostCache runs cmd in busybox with every mount backed by hostCacheTestID.
func solveWithHostCache(t *testing.T, sb integration.Sandbox, c *Client, cmd string, runOpts []llb.RunOption, mounts ...hostCacheTestMount) error {
	t.Helper()
	run := llb.Image("busybox:latest").Run(append([]llb.RunOption{llb.Shlex(cmd)}, runOpts...)...)
	for _, m := range mounts {
		base := m.base
		if base == nil {
			s := llb.Scratch()
			base = &s
		}
		opts := append([]llb.MountOption{llb.AsPersistentCacheDir(hostCacheTestID, m.sharing)}, m.opts...)
		run.AddMount(m.dest, *base, opts...)
	}
	def, err := run.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	return err
}

type hostCacheTestMount struct {
	dest    string
	base    *llb.State
	sharing llb.CacheMountSharingMode
	opts    []llb.MountOption
}

func hostMount(dest string, sharing llb.CacheMountSharingMode) hostCacheTestMount {
	return hostCacheTestMount{dest: dest, sharing: sharing}
}

// uidMount mounts the cache the way the Dockerfile frontend applies uid, gid
// and mode: on top of a directory created with them.
func uidMount(dest string, uid, gid int, mode os.FileMode) hostCacheTestMount {
	base := llb.Scratch().File(llb.Mkdir("/cache", mode, llb.WithUIDGID(uid, gid)))
	return hostCacheTestMount{dest: dest, base: &base, sharing: llb.CacheMountShared, opts: []llb.MountOption{llb.SourcePath("/cache")}}
}

// pruneAll removes every BuildKit cache record, which clears snapshot cache
// mounts: whatever a later build still finds in the cache came from the host.
func pruneAll(t *testing.T, sb integration.Sandbox, c *Client) {
	t.Helper()
	require.NoError(t, c.Prune(sb.Context(), nil, PruneAll))
}

func testHostCacheMountNoCache(t *testing.T, sb integration.Sandbox) {
	requireHostCacheMounts(t, sb)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	m := hostMount("/m", llb.CacheMountShared)
	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "touch /m/foo"`, nil, m))
	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "[ ! -e /m/foo ]; touch /m/foo2"`, []llb.RunOption{llb.IgnoreCache}, m))
	pruneAll(t, sb, c)
	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "[ -f /m/foo2 ]; [ ! -e /m/foo ]"`, nil, m))
}

func testHostCacheMountBases(t *testing.T, sb integration.Sandbox) {
	requireHostCacheMounts(t, sb)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	plain := hostMount("/m", llb.CacheMountShared)
	uid1000 := uidMount("/m", 1000, 1000, 0o750)
	uid2000 := uidMount("/m", 2000, 2000, 0o700)

	// Mounts of one ID with different bases are separate caches, as with
	// snapshot cache mounts, in whichever order they are first used.
	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "echo plain > /m/plain"`, nil, plain))
	require.NoError(t, solveWithHostCache(t, sb, c,
		`sh -e -c "[ \"$(stat -c %u:%g:%a /m)\" = 1000:1000:750 ]; [ ! -e /m/plain ]; touch /m/uid1000"`,
		[]llb.RunOption{llb.User("1000:1000")}, uid1000))
	require.NoError(t, solveWithHostCache(t, sb, c,
		`sh -e -c "[ -f /m/plain ]; [ ! -e /m/cache ]; [ ! -e /m/uid1000 ]"`, nil, plain))
	require.NoError(t, solveWithHostCache(t, sb, c,
		`sh -e -c "[ \"$(stat -c %u:%g:%a /m)\" = 2000:2000:700 ]; [ ! -e /m/uid1000 ]; touch /m/uid2000"`,
		[]llb.RunOption{llb.User("2000:2000")}, uid2000))

	pruneAll(t, sb, c)
	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "[ -f /m/uid1000 ]; [ ! -e /m/uid2000 ]"`,
		[]llb.RunOption{llb.User("1000:1000")}, uid1000))
	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "[ -f /m/plain ] && echo still-plain"`, nil, plain))
}

func testHostCacheMountRejectsSeededBase(t *testing.T, sb integration.Sandbox) {
	requireHostCacheMounts(t, sb)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	image := llb.Image("busybox:latest")
	seeded := hostCacheTestMount{dest: "/m", base: &image, sharing: llb.CacheMountShared, opts: []llb.MountOption{llb.SourcePath("/etc")}}
	err = solveWithHostCache(t, sb, c, `sh -e -c "ls /m"`, nil, seeded)
	require.ErrorContains(t, err, "can't start from the non-empty directory /etc")
}

func testHostCacheMountTwiceInOneExec(t *testing.T, sb integration.Sandbox) {
	requireHostCacheMounts(t, sb)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "echo x > /a/f; [ \"$(cat /b/f)\" = x ]"`, nil,
		hostMount("/a", llb.CacheMountLocked), hostMount("/b", llb.CacheMountLocked)))
	pruneAll(t, sb, c)
	require.NoError(t, solveWithHostCache(t, sb, c, `sh -e -c "[ -f /m/f ]"`, nil, hostMount("/m", llb.CacheMountLocked)))
}

func testHostCacheMountGatewayContainer(t *testing.T, sb integration.Sandbox) {
	requireHostCacheMounts(t, sb)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	run := busybox.Run(llb.Shlex(`sh -e -c "echo from-exec > /m/file"`))
	run.AddMount("/m", llb.Scratch(), llb.AsPersistentCacheDir(hostCacheTestID, llb.CacheMountShared))
	def, err := run.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	b := func(ctx context.Context, gc gateway.Client) (*gateway.Result, error) {
		def, err := busybox.Marshal(ctx)
		if err != nil {
			return nil, err
		}
		r, err := gc.Solve(ctx, gateway.SolveRequest{Definition: def.ToPB()})
		if err != nil {
			return nil, err
		}
		ctr, err := gc.NewContainer(ctx, gateway.NewContainerRequest{Mounts: []gateway.Mount{
			{Dest: "/", MountType: pb.MountType_BIND, Ref: r.Ref},
			{Dest: "/m", MountType: pb.MountType_CACHE, CacheOpt: &pb.CacheOpt{ID: hostCacheTestID, Sharing: pb.CacheSharingOpt_SHARED}},
		}})
		if err != nil {
			return nil, err
		}
		defer ctr.Release(ctx)

		var stdout bytes.Buffer
		pid, err := ctr.Start(ctx, gateway.StartRequest{
			Args:   []string{"sh", "-e", "-c", "cat /m/file; echo from-container > /m/file2"},
			Stdout: &iohelper.NopWriteCloser{Writer: &stdout},
		})
		if err != nil {
			return nil, err
		}
		if err := pid.Wait(); err != nil {
			return nil, err
		}
		require.Equal(t, "from-exec", strings.TrimSpace(stdout.String()))

		dt, err := ctr.ReadFile(ctx, gateway.ReadContainerRequest{
			ReadRequest: gateway.ReadRequest{Filename: "/file2"},
			MountIndex:  1,
		})
		if err != nil {
			return nil, err
		}
		require.Equal(t, "from-container", strings.TrimSpace(string(dt)))
		return gateway.NewResult(), nil
	}
	_, err = c.Build(sb.Context(), SolveOpt{}, "buildkit_test", b, nil)
	require.NoError(t, err)

	pruneAll(t, sb, c)
	run = busybox.Run(llb.Shlex(`sh -e -c "[ \"$(cat /m/file2)\" = from-container ]"`))
	run.AddMount("/m", llb.Scratch(), llb.AsPersistentCacheDir(hostCacheTestID, llb.CacheMountShared))
	def, err = run.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}
