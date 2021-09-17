package git

import (
	"context"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/containerd/containerd/content/local"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/native"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestRepeatedFetch(t *testing.T) {
	testRepeatedFetch(t, false)
}
func TestRepeatedFetchKeepGitDir(t *testing.T) {
	testRepeatedFetch(t, true)
}

func testRepeatedFetch(t *testing.T, keepGitDir bool) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := context.TODO()

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	gs := setupGitSource(t, tmpdir)

	repodir, err := ioutil.TempDir("", "buildkit-gitsource")
	require.NoError(t, err)
	defer os.RemoveAll(repodir)

	repodir, err = setupGitRepo(repodir)
	require.NoError(t, err)

	id := &source.GitIdentifier{Remote: repodir, KeepGitDir: keepGitDir}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := 40
	if keepGitDir {
		expLen += 4
	}

	require.Equal(t, expLen, len(key1))
	require.Equal(t, 40, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, false, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := ioutil.ReadFile(filepath.Join(dir, "def"))
	require.NoError(t, err)

	require.Equal(t, "bar\n", string(dt))

	_, err = os.Lstat(filepath.Join(dir, "ghi"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	_, err = os.Lstat(filepath.Join(dir, "sub/subfile"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	// second fetch returns same dir
	id = &source.GitIdentifier{Remote: repodir, Ref: "master", KeepGitDir: keepGitDir}

	g, err = gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key2, pin2, _, _, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)

	require.Equal(t, key1, key2)
	require.Equal(t, pin1, pin2)

	ref2, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref2.Release(context.TODO())

	require.Equal(t, ref1.ID(), ref2.ID())

	id = &source.GitIdentifier{Remote: repodir, Ref: "feature", KeepGitDir: keepGitDir}

	g, err = gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key3, pin3, _, _, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.NotEqual(t, key1, key3)
	require.NotEqual(t, pin1, pin3)

	ref3, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref3.Release(context.TODO())

	mount, err = ref3.Mount(ctx, false, nil)
	require.NoError(t, err)

	lm = snapshot.LocalMounter(mount)
	dir, err = lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err = ioutil.ReadFile(filepath.Join(dir, "ghi"))
	require.NoError(t, err)

	require.Equal(t, "baz\n", string(dt))

	dt, err = ioutil.ReadFile(filepath.Join(dir, "sub/subfile"))
	require.NoError(t, err)

	require.Equal(t, "subcontents\n", string(dt))
}

func TestFetchBySHA(t *testing.T) {
	testFetchBySHA(t, false)
}
func TestFetchBySHAKeepGitDir(t *testing.T) {
	testFetchBySHA(t, true)
}

func testFetchBySHA(t *testing.T, keepGitDir bool) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	gs := setupGitSource(t, tmpdir)

	repodir, err := ioutil.TempDir("", "buildkit-gitsource")
	require.NoError(t, err)
	defer os.RemoveAll(repodir)

	repodir, err = setupGitRepo(repodir)
	require.NoError(t, err)

	cmd := exec.Command("git", "rev-parse", "feature")
	cmd.Dir = repodir

	out, err := cmd.Output()
	require.NoError(t, err)

	sha := strings.TrimSpace(string(out))
	require.Equal(t, 40, len(sha))

	id := &source.GitIdentifier{Remote: repodir, Ref: sha, KeepGitDir: keepGitDir}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := 40
	if keepGitDir {
		expLen += 4
	}

	require.Equal(t, expLen, len(key1))
	require.Equal(t, 40, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, false, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := ioutil.ReadFile(filepath.Join(dir, "ghi"))
	require.NoError(t, err)

	require.Equal(t, "baz\n", string(dt))

	dt, err = ioutil.ReadFile(filepath.Join(dir, "sub/subfile"))
	require.NoError(t, err)

	require.Equal(t, "subcontents\n", string(dt))
}

func TestMultipleRepos(t *testing.T) {
	testMultipleRepos(t, false)
}

func TestMultipleReposKeepGitDir(t *testing.T) {
	testMultipleRepos(t, true)
}

func testMultipleRepos(t *testing.T, keepGitDir bool) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	gs := setupGitSource(t, tmpdir)

	repodir, err := ioutil.TempDir("", "buildkit-gitsource")
	require.NoError(t, err)
	defer os.RemoveAll(repodir)

	repodir, err = setupGitRepo(repodir)
	require.NoError(t, err)

	repodir2, err := ioutil.TempDir("", "buildkit-gitsource")
	require.NoError(t, err)
	defer os.RemoveAll(repodir2)

	err = runShell(repodir2,
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo xyz > xyz",
		"git add xyz",
		"git commit -m initial",
	)
	require.NoError(t, err)

	id := &source.GitIdentifier{Remote: repodir, KeepGitDir: keepGitDir}
	id2 := &source.GitIdentifier{Remote: repodir2, KeepGitDir: keepGitDir}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	g2, err := gs.Resolve(ctx, id2, nil, nil)
	require.NoError(t, err)

	expLen := 40
	if keepGitDir {
		expLen += 4
	}

	key1, pin1, _, _, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.Equal(t, expLen, len(key1))
	require.Equal(t, 40, len(pin1))

	key2, pin2, _, _, err := g2.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.Equal(t, expLen, len(key2))
	require.Equal(t, 40, len(pin2))

	require.NotEqual(t, key1, key2)
	require.NotEqual(t, pin1, pin2)

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, false, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	ref2, err := g2.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref2.Release(context.TODO())

	mount, err = ref2.Mount(ctx, false, nil)
	require.NoError(t, err)

	lm = snapshot.LocalMounter(mount)
	dir2, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := ioutil.ReadFile(filepath.Join(dir, "def"))
	require.NoError(t, err)

	require.Equal(t, "bar\n", string(dt))

	dt, err = ioutil.ReadFile(filepath.Join(dir2, "xyz"))
	require.NoError(t, err)

	require.Equal(t, "xyz\n", string(dt))
}

func TestCredentialRedaction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	gs := setupGitSource(t, tmpdir)

	url := "https://user:keepthissecret@non-existant-host/user/private-repo.git"
	id := &source.GitIdentifier{Remote: url}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	_, _, _, _, err = g.CacheKey(ctx, nil, 0)
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "keepthissecret"))
}

func TestSubdir(t *testing.T) {
	testSubdir(t, false)
}
func TestSubdirKeepGitDir(t *testing.T) {
	testSubdir(t, true)
}

func testSubdir(t *testing.T, keepGitDir bool) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := context.TODO()

	tmpdir, err := ioutil.TempDir("", "buildkit-state")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	gs := setupGitSource(t, tmpdir)

	repodir, err := ioutil.TempDir("", "buildkit-gitsource")
	require.NoError(t, err)
	defer os.RemoveAll(repodir)

	err = runShell(repodir,
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo foo > abc",
		"mkdir sub",
		"echo abc > sub/bar",
		"git add abc sub",
		"git commit -m initial",
	)
	require.NoError(t, err)

	id := &source.GitIdentifier{Remote: repodir, KeepGitDir: keepGitDir, Subdir: "sub"}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := 44
	if keepGitDir {
		expLen += 4
	}

	require.Equal(t, expLen, len(key1))
	require.Equal(t, 40, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, false, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	fis, err := ioutil.ReadDir(dir)
	require.NoError(t, err)

	require.Equal(t, 1, len(fis))

	dt, err := ioutil.ReadFile(filepath.Join(dir, "bar"))
	require.NoError(t, err)

	require.Equal(t, "abc\n", string(dt))
}

func setupGitSource(t *testing.T, tmpdir string) source.Source {
	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	assert.NoError(t, err)

	store, err := local.NewStore(tmpdir)
	require.NoError(t, err)

	db, err := bolt.Open(filepath.Join(tmpdir, "containerdmeta.db"), 0644, nil)
	require.NoError(t, err)

	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{
		"native": snapshotter,
	})

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	require.NoError(t, err)

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter("native", containerdsnapshot.NSSnapshotter("buildkit", mdb.Snapshotter("native")), nil),
		MetadataStore:  md,
		LeaseManager:   leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), "buildkit"),
		ContentStore:   mdb.ContentStore(),
		GarbageCollect: mdb.GarbageCollect,
	})
	require.NoError(t, err)

	gs, err := NewSource(Opt{
		CacheAccessor: cm,
	})
	require.NoError(t, err)

	return gs
}

func setupGitRepo(dir string) (string, error) {
	subPath := filepath.Join(dir, "sub")
	mainPath := filepath.Join(dir, "main")

	if err := os.MkdirAll(subPath, 0700); err != nil {
		return "", err
	}

	if err := os.MkdirAll(mainPath, 0700); err != nil {
		return "", err
	}

	if err := runShell(filepath.Join(dir, "sub"),
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo subcontents > subfile",
		"git add subfile",
		"git commit -m initial",
	); err != nil {
		return "", err
	}
	if err := runShell(filepath.Join(dir, "main"),
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo foo > abc",
		"git add abc",
		"git commit -m initial",
		"echo bar > def",
		"git add def",
		"git commit -m second",
		"git checkout -B feature",
		"echo baz > ghi",
		"git add ghi",
		"git commit -m feature",
		"git submodule add "+subPath+" sub",
		"git add -A",
		"git commit -m withsub",
		"git checkout master",
	); err != nil {
		return "", err
	}
	return mainPath, nil
}

func runShell(dir string, cmds ...string) error {
	for _, args := range cmds {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell", "-command", args)
		} else {
			cmd = exec.Command("sh", "-c", args)
		}
		cmd.Dir = dir
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "error running %v", args)
		}
	}
	return nil
}
