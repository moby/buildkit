package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/diff/apply"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/diff/walking"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/gitutil"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestRepeatedFetchSHA1(t *testing.T) {
	testRepeatedFetch(t, false, "sha1")
}
func TestRepeatedFetchKeepGitDirSHA1(t *testing.T) {
	testRepeatedFetch(t, true, "sha1")
}

func TestRepeatedFetchSHA256(t *testing.T) {
	testRepeatedFetch(t, false, "sha256")
}
func TestRepeatedFetchKeepGitDirSHA256(t *testing.T) {
	testRepeatedFetch(t, true, "sha256")
}

func testRepeatedFetch(t *testing.T, keepGitDir bool, format string) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := logProgressStreams(context.Background(), t)

	gs := setupGitSource(t, t.TempDir())

	repo := setupGitRepo(t, format)

	id := &GitIdentifier{Remote: repo.mainURL, KeepGitDir: keepGitDir}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := 40
	if format == "sha256" {
		expLen = 64
	}
	expPinLen := expLen
	if keepGitDir {
		expLen += 4
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, expPinLen, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := os.ReadFile(filepath.Join(dir, "def"))
	require.NoError(t, err)

	require.Equal(t, "bar\n", string(dt))

	_, err = os.Lstat(filepath.Join(dir, "ghi"))
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = os.Lstat(filepath.Join(dir, "sub/subfile"))
	require.ErrorIs(t, err, os.ErrNotExist)

	// second fetch returns same dir
	id = &GitIdentifier{Remote: repo.mainURL, Ref: "master", KeepGitDir: keepGitDir}

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

	id = &GitIdentifier{Remote: repo.mainURL, Ref: "feature", KeepGitDir: keepGitDir}

	g, err = gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key3, pin3, _, _, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.NotEqual(t, key1, key3)
	require.NotEqual(t, pin1, pin3)

	ref3, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref3.Release(context.TODO())

	mount, err = ref3.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm = snapshot.LocalMounter(mount)
	dir, err = lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err = os.ReadFile(filepath.Join(dir, "ghi"))
	require.NoError(t, err)

	require.Equal(t, "baz\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(dir, "sub/subfile"))
	require.NoError(t, err)

	require.Equal(t, "subcontents\n", string(dt))

	// The key should not change regardless to the existence of Checksum
	// https://github.com/moby/buildkit/pull/5975#discussion_r2092206059
	id.Checksum = pin3
	g, err = gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)
	key4, pin4, _, _, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.Equal(t, key3, key4)
	require.Equal(t, pin3, pin4)
}

func TestFetchBySHA1(t *testing.T) {
	testFetchBySHA(t, "sha1", false)
}
func TestFetchBySHA1KeepGitDir(t *testing.T) {
	testFetchBySHA(t, "sha1", true)
}
func TestFetchBySHA256(t *testing.T) {
	testFetchBySHA(t, "sha256", false)
}
func TestFetchBySHA256KeepGitDir(t *testing.T) {
	testFetchBySHA(t, "sha256", true)
}

func testFetchBySHA(t *testing.T, format string, keepGitDir bool) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	gs := setupGitSource(t, t.TempDir())

	repo := setupGitRepo(t, format)

	cmd := exec.Command("git", "rev-parse", "feature")
	cmd.Dir = repo.mainPath

	out, err := cmd.Output()
	require.NoError(t, err)

	var shaLen int
	switch format {
	case "sha1":
		shaLen = 40
	case "sha256":
		shaLen = 64
	default:
		t.Fatalf("unexpected format: %q", format)
	}

	sha := strings.TrimSpace(string(out))
	require.Equal(t, shaLen, len(sha))

	id := &GitIdentifier{Remote: repo.mainURL, Ref: sha, KeepGitDir: keepGitDir}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := shaLen
	if keepGitDir {
		expLen += 4
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, shaLen, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := os.ReadFile(filepath.Join(dir, "ghi"))
	require.NoError(t, err)

	require.Equal(t, "baz\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(dir, "sub/subfile"))
	require.NoError(t, err)

	require.Equal(t, "subcontents\n", string(dt))
}

func TestFetchUnreferencedTagShaSHA1(t *testing.T) {
	testFetchUnreferencedRefSha(t, "v1.2.3-special", false, "sha1")
}

func TestFetchUnreferencedTagShaKeepGitDirSHA1(t *testing.T) {
	testFetchUnreferencedRefSha(t, "v1.2.3-special", true, "sha1")
}

func TestFetchUnreferencedRefShaSHA1(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special", false, "sha1")
}

func TestFetchUnreferencedRefShaKeepGitDirSHA1(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special", true, "sha1")
}

func TestFetchUnadvertisedRefShaSHA1(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special~", false, "sha1")
}

func TestFetchUnadvertisedRefShaKeepGitDirSHA1(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special~", true, "sha1")
}

func TestFetchUnreferencedTagShaSHA256(t *testing.T) {
	testFetchUnreferencedRefSha(t, "v1.2.3-special", false, "sha256")
}

func TestFetchUnreferencedTagShaKeepGitDirSHA256(t *testing.T) {
	testFetchUnreferencedRefSha(t, "v1.2.3-special", true, "sha256")
}

func TestFetchUnreferencedRefShaSHA256(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special", false, "sha256")
}

func TestFetchUnreferencedRefShaKeepGitDirSHA256(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special", true, "sha256")
}

func TestFetchUnadvertisedRefShaSHA256(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special~", false, "sha256")
}

func TestFetchUnadvertisedRefShaKeepGitDirSHA256(t *testing.T) {
	testFetchUnreferencedRefSha(t, "refs/special~", true, "sha256")
}

// testFetchUnreferencedRefSha tests fetching a SHA that points to a ref that is not reachable from any branch.
func testFetchUnreferencedRefSha(t *testing.T, ref string, keepGitDir bool, format string) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	gs := setupGitSource(t, t.TempDir())

	repo := setupGitRepo(t, format)

	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repo.mainPath

	out, err := cmd.Output()
	require.NoError(t, err)

	expSHALen := 40
	if format == "sha256" {
		expSHALen = 64
	}

	sha := strings.TrimSpace(string(out))
	require.Equal(t, expSHALen, len(sha))

	id := &GitIdentifier{Remote: repo.mainURL, Ref: sha, KeepGitDir: keepGitDir}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := 40
	if format == "sha256" {
		expLen = 64
	}
	expPinLen := expLen
	if keepGitDir {
		expLen += 4
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, expPinLen, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := os.ReadFile(filepath.Join(dir, "bar"))
	require.NoError(t, err)

	require.Equal(t, "foo\n", string(dt))
}

func TestFetchByTagSHA1(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeNone, "sha1")
}

func TestFetchByTagKeepGitDirSHA1(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, true, testChecksumModeNone, "sha1")
}

func TestFetchByTagFullSHA1(t *testing.T) {
	testFetchByTag(t, "refs/tags/lightweight-tag", "third", false, true, true, testChecksumModeNone, "sha1")
}

func TestFetchByAnnotatedTagSHA1(t *testing.T) {
	testFetchByTag(t, "v1.2.3", "second", true, false, false, testChecksumModeNone, "sha1")
}

func TestFetchByAnnotatedTagKeepGitDirSHA1(t *testing.T) {
	testFetchByTag(t, "v1.2.3", "second", true, false, true, testChecksumModeNone, "sha1")
}

func TestFetchByAnnotatedTagFullSHA1(t *testing.T) {
	testFetchByTag(t, "refs/tags/v1.2.3", "second", true, false, true, testChecksumModeNone, "sha1")
}

func TestFetchByBranchSHA1(t *testing.T) {
	testFetchByTag(t, "feature", "withsub", false, true, false, testChecksumModeNone, "sha1")
}

func TestFetchByBranchKeepGitDirSHA1(t *testing.T) {
	testFetchByTag(t, "feature", "withsub", false, true, true, testChecksumModeNone, "sha1")
}

func TestFetchByBranchFullSHA1(t *testing.T) {
	testFetchByTag(t, "refs/heads/feature", "withsub", false, true, true, testChecksumModeNone, "sha1")
}

func TestFetchByRefSHA1(t *testing.T) {
	testFetchByTag(t, "test", "feature", false, true, false, testChecksumModeNone, "sha1")
}

func TestFetchByRefKeepGitDirSHA1(t *testing.T) {
	testFetchByTag(t, "test", "feature", false, true, true, testChecksumModeNone, "sha1")
}

func TestFetchByRefFullSHA1(t *testing.T) {
	testFetchByTag(t, "refs/test", "feature", false, true, true, testChecksumModeNone, "sha1")
}

func TestFetchByTagWithChecksumSHA1(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeValid, "sha1")
}

func TestFetchByTagWithChecksumPartialSHA1(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeValidPartial, "sha1")
}

func TestFetchByTagWithChecksumInvalidSHA1(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeInvalid, "sha1")
}

func TestFetchByTagSHA256(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeNone, "sha256")
}

func TestFetchByTagKeepGitDirSHA256(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, true, testChecksumModeNone, "sha256")
}

func TestFetchByTagFullSHA256(t *testing.T) {
	testFetchByTag(t, "refs/tags/lightweight-tag", "third", false, true, true, testChecksumModeNone, "sha256")
}

func TestFetchByAnnotatedTagSHA256(t *testing.T) {
	testFetchByTag(t, "v1.2.3", "second", true, false, false, testChecksumModeNone, "sha256")
}

func TestFetchByAnnotatedTagKeepGitDirSHA256(t *testing.T) {
	testFetchByTag(t, "v1.2.3", "second", true, false, true, testChecksumModeNone, "sha256")
}

func TestFetchByAnnotatedTagFullSHA256(t *testing.T) {
	testFetchByTag(t, "refs/tags/v1.2.3", "second", true, false, true, testChecksumModeNone, "sha256")
}

func TestFetchByBranchSHA256(t *testing.T) {
	testFetchByTag(t, "feature", "withsub", false, true, false, testChecksumModeNone, "sha256")
}

func TestFetchByBranchKeepGitDirSHA256(t *testing.T) {
	testFetchByTag(t, "feature", "withsub", false, true, true, testChecksumModeNone, "sha256")
}

func TestFetchByBranchFullSHA256(t *testing.T) {
	testFetchByTag(t, "refs/heads/feature", "withsub", false, true, true, testChecksumModeNone, "sha256")
}

func TestFetchByRefSHA256(t *testing.T) {
	testFetchByTag(t, "test", "feature", false, true, false, testChecksumModeNone, "sha256")
}

func TestFetchByRefKeepGitDirSHA256(t *testing.T) {
	testFetchByTag(t, "test", "feature", false, true, true, testChecksumModeNone, "sha256")
}

func TestFetchByRefFullSHA256(t *testing.T) {
	testFetchByTag(t, "refs/test", "feature", false, true, true, testChecksumModeNone, "sha256")
}

func TestFetchByTagWithChecksumSHA256(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeValid, "sha256")
}

func TestFetchByTagWithChecksumPartialSHA256(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeValidPartial, "sha256")
}

func TestFetchByTagWithChecksumInvalidSHA256(t *testing.T) {
	testFetchByTag(t, "lightweight-tag", "third", false, true, false, testChecksumModeInvalid, "sha256")
}

type testChecksumMode int

const (
	testChecksumModeNone testChecksumMode = iota
	testChecksumModeValid
	testChecksumModeValidPartial
	testChecksumModeInvalid
)

func testFetchByTag(t *testing.T, tag, expectedCommitSubject string, isAnnotatedTag, hasFoo13File, keepGitDir bool, checksumMode testChecksumMode, format string) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	gs := setupGitSource(t, t.TempDir())

	repo := setupGitRepo(t, format)

	id := &GitIdentifier{Remote: repo.mainURL, Ref: tag, KeepGitDir: keepGitDir}

	if checksumMode != testChecksumModeNone {
		cmd := exec.Command("git", "rev-parse", tag)
		cmd.Dir = repo.mainPath

		out, err := cmd.Output()
		require.NoError(t, err)

		expLen := 40
		if format == "sha256" {
			expLen = 64
		}
		sha := strings.TrimSpace(string(out))
		require.Equal(t, expLen, len(sha))

		switch checksumMode {
		case testChecksumModeValid:
			id.Checksum = sha
		case testChecksumModeValidPartial:
			id.Checksum = sha[:8]
		case testChecksumModeInvalid:
			id.Checksum = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
			if format == "sha256" {
				id.Checksum = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
			}
		default:
			// NOTREACHED
		}
	}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	if checksumMode == testChecksumModeInvalid {
		require.ErrorContains(t, err, "expected checksum to match "+id.Checksum)
		return
	}
	require.NoError(t, err)
	require.True(t, done)

	expLen := 40
	if format == "sha256" {
		expLen = 64
	}
	expPinLen := expLen
	if keepGitDir {
		expLen += 4
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, expPinLen, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	st, err := os.Lstat(filepath.Join(dir, "subdir"))
	require.NoError(t, err)

	require.True(t, st.IsDir())
	require.Equal(t, strconv.FormatInt(0755, 8), strconv.FormatInt(int64(st.Mode()&os.ModePerm), 8))

	dt, err := os.ReadFile(filepath.Join(dir, "def"))
	require.NoError(t, err)
	require.Equal(t, "bar\n", string(dt))

	st, err = os.Lstat(filepath.Join(dir, "def"))
	require.NoError(t, err)

	require.Equal(t, strconv.FormatInt(0644, 8), strconv.FormatInt(int64(st.Mode()&os.ModePerm), 8))

	dt, err = os.ReadFile(filepath.Join(dir, "foo13"))
	if hasFoo13File {
		require.NoError(t, err)
		require.Equal(t, "sbb\n", string(dt))
	} else {
		require.ErrorIs(t, err, os.ErrNotExist)
	}

	if keepGitDir {
		git := gitutil.NewGitCLI(
			gitutil.WithExec(runWithStandardUmask),
			gitutil.WithStreams(func(ctx context.Context) (stdout, stderr io.WriteCloser, flush func()) {
				return logs.NewLogStreams(ctx, false)
			}),
			gitutil.WithWorkTree(dir),
		)

		// get current commit sha
		headCommit, err := git.Run(ctx, "rev-parse", "HEAD")
		require.NoError(t, err)

		if isAnnotatedTag {
			// get commit sha that the annotated tag points to
			annotatedTagCommit, err := git.Run(ctx, "rev-list", "-n", "1", tag)
			require.NoError(t, err)

			// HEAD should match the actual commit sha (and not the sha of the annotated tag,
			// since it's not possible to checkout a non-commit object)
			require.Equal(t, string(annotatedTagCommit), string(headCommit))

			annotatedTagSHA, err := git.Run(ctx, "rev-parse", tag)
			require.NoError(t, err)

			require.Equal(t, strings.TrimSpace(string(annotatedTagSHA)), pin1)
		} else {
			// ensure that we checked out the same commit as was in the cache key
			require.Equal(t, strings.TrimSpace(string(headCommit)), pin1)
		}

		// test that we checked out the correct commit
		// (in the case of an annotated tag, this message is of the commit the annotated tag points to
		// and not the message of the tag)
		gitLogOutput, err := git.Run(ctx, "log", "-n", "1", "--format=%s")
		require.NoError(t, err)
		require.Contains(t, strings.TrimSpace(string(gitLogOutput)), expectedCommitSubject)
	}
}
func TestFetchAnnotatedTagAfterCloneSHA1(t *testing.T) {
	testFetchAnnotatedTagAfterClone(t, "sha1")
}

func TestFetchAnnotatedTagAfterCloneSHA256(t *testing.T) {
	testFetchAnnotatedTagAfterClone(t, "sha256")
}

func testFetchAnnotatedTagAfterClone(t *testing.T, format string) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	repo := setupGitRepo(t, format)
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo.mainPath

	out, err := cmd.Output()
	require.NoError(t, err)

	expLen := 40
	if format == "sha256" {
		expLen = 64
	}
	sha := strings.TrimSpace(string(out))
	require.Equal(t, expLen, len(sha))

	gs := setupGitSource(t, t.TempDir())

	id := &GitIdentifier{Remote: repo.mainURL, Ref: sha, KeepGitDir: true}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	require.GreaterOrEqual(t, len(key1), expLen+4)
	require.Equal(t, expLen, len(pin1))

	ref, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	ref.Release(context.TODO())

	id = &GitIdentifier{Remote: repo.mainURL, Ref: "refs/tags/v1.2.3", KeepGitDir: true}
	g, err = gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err = g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	require.GreaterOrEqual(t, len(key1), expLen+4)
	require.Equal(t, expLen, len(pin1))

	ref, err = g.Snapshot(ctx, nil)
	require.NoError(t, err)
	ref.Release(context.TODO())
}

func TestFetchAnnotatedTagChecksumsSHA1(t *testing.T) {
	testFetchAnnotatedTagChecksums(t, "sha1", false)
}

func TestFetchAnnotatedTagChecksumsSHA256(t *testing.T) {
	testFetchAnnotatedTagChecksums(t, "sha256", false)
}

func TestFetchAnnotatedTagChecksumsKeepGitDirSHA1(t *testing.T) {
	testFetchAnnotatedTagChecksums(t, "sha1", true)
}

func TestFetchAnnotatedTagChecksumsKeepGitDirSHA256(t *testing.T) {
	testFetchAnnotatedTagChecksums(t, "sha256", true)
}

func testFetchAnnotatedTagChecksums(t *testing.T, format string, keepGitDir bool) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	repo := setupGitRepo(t, format)
	cmd := exec.Command("git", "rev-parse", "v1.2.3")
	cmd.Dir = repo.mainPath

	out, err := cmd.Output()
	require.NoError(t, err)

	expLen := 40
	if format == "sha256" {
		expLen = 64
	}
	shaTag := strings.TrimSpace(string(out))
	require.Equal(t, expLen, len(shaTag))

	// make sure this is an annotated tag
	cmd = exec.Command("git", "cat-file", "-t", shaTag)
	cmd.Dir = repo.mainPath

	out, err = cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "tag\n", string(out))

	// get commit that the tag points to
	cmd = exec.Command("git", "rev-parse", "v1.2.3^{}")
	cmd.Dir = repo.mainPath

	out, err = cmd.Output()
	require.NoError(t, err)

	shaCommit := strings.TrimSpace(string(out))
	require.Equal(t, expLen, len(shaCommit))

	require.NotEqual(t, shaTag, shaCommit)

	gs := setupGitSource(t, t.TempDir())

	tcases := []*GitIdentifier{
		{Remote: repo.mainURL, Ref: "v1.2.3", KeepGitDir: keepGitDir},
		{Remote: repo.mainURL, Ref: "refs/tags/v1.2.3", KeepGitDir: keepGitDir},
		{Remote: repo.mainURL, Ref: "v1.2.3", KeepGitDir: keepGitDir, Checksum: shaTag},
		{Remote: repo.mainURL, Ref: "v1.2.3", KeepGitDir: keepGitDir, Checksum: shaCommit},
	}

	for _, id := range tcases {
		g, err := gs.Resolve(ctx, id, nil, nil)
		require.NoError(t, err)

		key, pin, _, done, err := g.CacheKey(ctx, nil, 0)
		require.NoError(t, err)
		require.True(t, done)

		if keepGitDir {
			require.GreaterOrEqual(t, len(key), expLen+4)
		} else {
			require.Equal(t, expLen, len(key))
		}
		require.Equal(t, expLen, len(pin))

		require.Equal(t, shaTag, pin)

		// key is based on commit sha if keepGitDir is false
		if keepGitDir {
			require.Equal(t, shaTag+".git#refs/tags/v1.2.3", key)
		} else {
			require.Equal(t, shaCommit, key)
		}

		ref, err := g.Snapshot(ctx, nil)
		require.NoError(t, err)
		ref.Release(context.TODO())

		if keepGitDir {
			mountable, err := ref.Mount(ctx, true, nil)
			require.NoError(t, err)

			lm1 := snapshot.LocalMounter(mountable)
			dir, err := lm1.Mount()
			require.NoError(t, err)
			defer lm1.Unmount()

			cmd = exec.Command("git", "tag", "--points-at", "HEAD")
			cmd.Dir = dir

			out, err = cmd.Output()
			require.NoError(t, err)
			require.Equal(t, "v1.2.3\n", string(out))

			cmd = exec.Command("git", "rev-parse", "v1.2.3")
			cmd.Dir = dir

			out, err = cmd.Output()
			require.NoError(t, err)
			require.Equal(t, shaTag, strings.TrimSpace(string(out)))
		}
	}
}

func TestFetchMutatedTagSHA1(t *testing.T) {
	testFetchMutatedTag(t, "sha1", false)
}

func TestFetchMutatedTagKeepGitDirSHA1(t *testing.T) {
	testFetchMutatedTag(t, "sha1", true)
}

func TestFetchMutatedTagSHA256(t *testing.T) {
	testFetchMutatedTag(t, "sha256", false)
}

func TestFetchMutatedTagKeepGitDirSHA256(t *testing.T) {
	testFetchMutatedTag(t, "sha256", true)
}

func testFetchMutatedTag(t *testing.T, format string, keepGitDir bool) {
	ctx := t.Context()
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}
	repo := setupGitRepo(t, format)
	cmd := exec.Command("git", "rev-parse", "v1.2.3")
	cmd.Dir = repo.mainPath

	out, err := cmd.Output()
	require.NoError(t, err)

	expLen := 40
	if format == "sha256" {
		expLen = 64
	}
	shaTag := strings.TrimSpace(string(out))
	require.Equal(t, expLen, len(shaTag))

	id := &GitIdentifier{Remote: repo.mainURL, Ref: shaTag, KeepGitDir: keepGitDir}
	gs := setupGitSource(t, t.TempDir())

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	ref, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	ref.Release(context.TODO())

	// mutate the tag to point to another commit
	cmd = exec.Command("git", "tag", "-f", "v1.2.3", "feature")
	cmd.Dir = repo.mainPath
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	// verify that the tag now points to a different commit
	cmd = exec.Command("git", "rev-parse", "v1.2.3")
	cmd.Dir = repo.mainPath

	out, err = cmd.Output()
	require.NoError(t, err)

	shaTagMutated := strings.TrimSpace(string(out))
	require.Equal(t, expLen, len(shaTagMutated))
	require.NotEqual(t, shaTag, shaTagMutated)

	id = &GitIdentifier{Remote: repo.mainURL, Ref: shaTagMutated, KeepGitDir: keepGitDir}

	// fetching the original tag should still return the original commit because of caching
	g, err = gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key2, pin2, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	require.NotEqual(t, key1, key2)
	require.NotEqual(t, pin1, pin2)

	ref, err = g.Snapshot(ctx, nil)
	require.NoError(t, err)
	ref.Release(context.TODO())
}

func TestMultipleTagAccessKeepGitDirSHA1(t *testing.T) {
	testMultipleTagAccess(t, true, "sha1")
}

func TestMultipleTagAccessSHA1(t *testing.T) {
	testMultipleTagAccess(t, false, "sha1")
}

func TestMultipleTagAccessKeepGitDirSHA256(t *testing.T) {
	testMultipleTagAccess(t, true, "sha256")
}

func TestMultipleTagAccessSHA256(t *testing.T) {
	testMultipleTagAccess(t, false, "sha256")
}

func testMultipleTagAccess(t *testing.T, keepGitDir bool, format string) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	gs := setupGitSource(t, t.TempDir())

	repo := setupGitRepo(t, format)

	id := &GitIdentifier{Remote: repo.mainURL, KeepGitDir: keepGitDir, Ref: "a/v1.2.3"}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	expLen := 40
	if format == "sha256" {
		expLen = 64
	}
	expPinLen := expLen
	if keepGitDir {
		expLen += 4
	}

	key1, pin1, _, _, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	if keepGitDir {
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, expPinLen, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	id2 := &GitIdentifier{Remote: repo.mainURL, KeepGitDir: keepGitDir, Ref: "a/v1.2.3-same"}
	g2, err := gs.Resolve(ctx, id2, nil, nil)
	require.NoError(t, err)

	key2, pin2, _, _, err := g2.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	if keepGitDir {
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, expPinLen, len(pin2))

	require.Equal(t, pin1, pin2)
	if !keepGitDir {
		require.Equal(t, key1, key2)
		return
	}
	// key should be different because of the ref
	require.NotEqual(t, key1, key2)

	ref2, err := g2.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount1, err := ref2.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm1 := snapshot.LocalMounter(mount1)
	dir1, err := lm1.Mount()
	require.NoError(t, err)
	defer lm1.Unmount()

	workDir := t.TempDir()

	runShell(t, dir1, fmt.Sprintf(`git rev-parse a/v1.2.3 > %s/ref1`, workDir))

	dt1, err := os.ReadFile(filepath.Join(workDir, "ref1"))
	require.NoError(t, err)

	mount2, err := ref2.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm2 := snapshot.LocalMounter(mount2)
	dir2, err := lm2.Mount()
	require.NoError(t, err)
	defer lm2.Unmount()

	runShell(t, dir2, fmt.Sprintf(`git rev-parse a/v1.2.3-same > %s/ref2`, workDir))

	dt2, err := os.ReadFile(filepath.Join(workDir, "ref2"))
	require.NoError(t, err)
	require.Equal(t, string(dt1), string(dt2))
}

func TestMultipleReposSHA1(t *testing.T) {
	testMultipleRepos(t, false, "sha1")
}

func TestMultipleReposKeepGitDir(t *testing.T) {
	testMultipleRepos(t, true, "sha1")
}

func TestMultipleReposSHA256(t *testing.T) {
	testMultipleRepos(t, false, "sha256")
}

func TestMultipleReposKeepGitDirSHA256(t *testing.T) {
	testMultipleRepos(t, true, "sha256")
}

func testMultipleRepos(t *testing.T, keepGitDir bool, format string) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	gs := setupGitSource(t, t.TempDir())

	repo := setupGitRepo(t, format)

	repodir2 := t.TempDir()

	runShell(t, repodir2,
		"git -c init.defaultBranch=master init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo xyz > xyz",
		"git add xyz",
		"git commit -m initial",
	)
	repoURL2 := serveGitRepo(t, repodir2)

	id := &GitIdentifier{Remote: repo.mainURL, KeepGitDir: keepGitDir}
	id2 := &GitIdentifier{Remote: repoURL2, KeepGitDir: keepGitDir}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	g2, err := gs.Resolve(ctx, id2, nil, nil)
	require.NoError(t, err)

	expLen := 40
	expLen2 := expLen
	if format == "sha256" {
		expLen = 64
	}
	expPinLen := expLen
	if keepGitDir {
		expLen += 4
		expLen2 += 4
	}

	key1, pin1, _, _, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	if keepGitDir {
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, expPinLen, len(pin1))

	key2, pin2, _, _, err := g2.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	if keepGitDir {
		require.GreaterOrEqual(t, len(key2), expLen2)
	} else {
		require.Equal(t, expLen2, len(key2))
	}
	require.Equal(t, 40, len(pin2))

	require.NotEqual(t, key1, key2)
	require.NotEqual(t, pin1, pin2)

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	ref2, err := g2.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref2.Release(context.TODO())

	mount, err = ref2.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm = snapshot.LocalMounter(mount)
	dir2, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := os.ReadFile(filepath.Join(dir, "def"))
	require.NoError(t, err)

	require.Equal(t, "bar\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(dir2, "xyz"))
	require.NoError(t, err)

	require.Equal(t, "xyz\n", string(dt))
}

func TestCredentialRedaction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}

	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	gs := setupGitSource(t, t.TempDir())

	url := "https://user:keepthissecret@non-existant-host/user/private-repo.git"
	id := &GitIdentifier{Remote: url}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	_, _, _, _, err = g.CacheKey(ctx, nil, 0)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "keepthissecret")
}

func TestSubmoduleSubdirSHA1(t *testing.T) {
	testSubmoduleSubdir(t, false, "sha1")
}

func TestSubmoduleSubdirKeepGitDirSHA1(t *testing.T) {
	testSubmoduleSubdir(t, true, "sha1")
}

func TestSubmoduleSubdirSHA256(t *testing.T) {
	testSubmoduleSubdir(t, false, "sha256")
}

func TestSubmoduleSubdirKeepGitDirSHA256(t *testing.T) {
	testSubmoduleSubdir(t, true, "sha256")
}

func testSubmoduleSubdir(t *testing.T, keepGitDir bool, format string) {
	if runtime.GOOS == "windows" {
		t.Skip("Depends on unimplemented containerd bind-mount support on Windows")
	}
	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")
	ctx = logProgressStreams(ctx, t)

	gs := setupGitSource(t, t.TempDir())

	repo := setupGitRepo(t, format)

	id := &GitIdentifier{Remote: repo.mainURL, KeepGitDir: keepGitDir, Ref: "feature", Subdir: "sub"}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := 44
	expPinLen := 40
	if format == "sha256" {
		expLen = 68
		expPinLen = 64
	}
	require.GreaterOrEqual(t, len(key1), expLen)
	require.Equal(t, expPinLen, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	dt, err := os.ReadFile(filepath.Join(dir, "subfile"))
	require.NoError(t, err)

	require.Equal(t, "subcontents\n", string(dt))
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

	ctx := logProgressStreams(context.Background(), t)

	gs := setupGitSource(t, t.TempDir())

	repodir := t.TempDir()

	runShell(t, repodir,
		"git -c init.defaultBranch=master init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo foo > abc",
		"mkdir sub",
		"echo abc > sub/bar",
		"git add abc sub",
		"git commit -m initial",
	)

	repoURL := serveGitRepo(t, repodir)
	id := &GitIdentifier{Remote: repoURL, KeepGitDir: keepGitDir, Subdir: "sub"}

	g, err := gs.Resolve(ctx, id, nil, nil)
	require.NoError(t, err)

	key1, pin1, _, done, err := g.CacheKey(ctx, nil, 0)
	require.NoError(t, err)
	require.True(t, done)

	expLen := 44
	if keepGitDir {
		expLen += 4
		require.GreaterOrEqual(t, len(key1), expLen)
	} else {
		require.Equal(t, expLen, len(key1))
	}
	require.Equal(t, 40, len(pin1))

	ref1, err := g.Snapshot(ctx, nil)
	require.NoError(t, err)
	defer ref1.Release(context.TODO())

	mount, err := ref1.Mount(ctx, true, nil)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	require.NoError(t, err)
	defer lm.Unmount()

	fis, err := os.ReadDir(dir)
	require.NoError(t, err)

	require.Equal(t, 1, len(fis))

	dt, err := os.ReadFile(filepath.Join(dir, "bar"))
	require.NoError(t, err)

	require.Equal(t, "abc\n", string(dt))
}

func setupGitSource(t *testing.T, tmpdir string) source.Source {
	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)

	store, err := local.NewStore(tmpdir)
	require.NoError(t, err)

	db, err := bolt.Open(filepath.Join(tmpdir, "containerdmeta.db"), 0644, nil)
	require.NoError(t, err)

	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{
		"native": snapshotter,
	})

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	require.NoError(t, err)
	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), "buildkit")
	c := mdb.ContentStore()
	applier := winlayers.NewFileSystemApplierWithWindows(c, apply.NewFileSystemApplier(c))
	differ := winlayers.NewWalkingDiffWithWindows(c, walking.NewWalkingDiff(c))

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter("native", containerdsnapshot.NSSnapshotter("buildkit", mdb.Snapshotter("native")), nil),
		MetadataStore:  md,
		LeaseManager:   lm,
		ContentStore:   c,
		Applier:        applier,
		Differ:         differ,
		GarbageCollect: mdb.GarbageCollect,
		Root:           tmpdir,
		MountPoolRoot:  filepath.Join(tmpdir, "cachemounts"),
	})
	require.NoError(t, err)

	gs, err := NewSource(Opt{
		CacheAccessor: cm,
	})
	require.NoError(t, err)

	return gs
}

type gitRepoFixture struct {
	mainPath, subPath string // Filesystem paths to the respective repos
	mainURL, subURL   string // HTTP URLs for the respective repos
}

func setupGitRepo(t *testing.T, format string) gitRepoFixture {
	t.Helper()
	dir := t.TempDir()
	srv := serveGitRepo(t, dir)
	fixture := gitRepoFixture{
		subPath:  filepath.Join(dir, "sub"),
		subURL:   srv + "/sub",
		mainPath: filepath.Join(dir, "main"),
		mainURL:  srv + "/main",
	}
	require.NoError(t, os.MkdirAll(fixture.subPath, 0700))
	require.NoError(t, os.MkdirAll(fixture.mainPath, 0700))

	runShell(t, fixture.subPath,
		"git -c init.defaultBranch=master init --object-format="+format,
		"git config --local user.email test",
		"git config --local user.name test",
		"echo subcontents > subfile",
		"git add subfile",
		"git commit -m initial",
	)
	// * (refs/heads/feature) withsub
	// * feature
	// * (HEAD -> refs/heads/master, tag: refs/tags/lightweight-tag) third
	// | * ref only
	// | * commit only
	// | * (tag: refs/tags/v1.2.3-special) tagonly-leaf
	// |/
	// * (tag: refs/tags/v1.2.3) second
	// * (tag: refs/tags/a/v1.2.3, refs/tags/a/v1.2.3-same) initial
	runShell(t, fixture.mainPath,
		"git -c init.defaultBranch=master init --object-format="+format,
		"git config --local user.email test",
		"git config --local user.name test",

		"echo foo > abc",
		"git add abc",
		"git commit -m initial",
		"git tag --no-sign a/v1.2.3",
		"git tag --no-sign a/v1.2.3-same",
		"echo bar > def",
		"mkdir subdir",
		"echo subcontents > subdir/subfile",
		"git add def subdir",
		"git commit -m second",
		"git tag -a -m \"this is an annotated tag\" v1.2.3",

		"echo foo > bar",
		"git add bar",
		"git commit -m tagonly-leaf",
		"git tag --no-sign v1.2.3-special",

		"echo foo2 > bar2",
		"git add bar2",
		"git commit -m \"commit only\"",
		"echo foo3 > bar3",
		"git add bar3",
		"git commit -m \"ref only\"",
		"git update-ref refs/special $(git rev-parse HEAD)",

		// switch master back to v1.2.3
		"git checkout -B master v1.2.3",

		"echo sbb > foo13",
		"git add foo13",
		"git commit -m third",
		"git tag --no-sign lightweight-tag",

		"git checkout -B feature",

		"echo baz > ghi",
		"git add ghi",
		"git commit -m feature",
		"git update-ref refs/test $(git rev-parse HEAD)",

		"git submodule add "+fixture.subURL+" sub",
		"git add -A",
		"git commit -m withsub",

		"git checkout master",

		// "git log --oneline --graph --decorate=full --all",
	)
	return fixture
}

func serveGitRepo(t *testing.T, root string) string {
	t.Helper()
	gitpath, err := exec.LookPath("git")
	require.NoError(t, err)
	gitversion, _ := exec.Command(gitpath, "version").CombinedOutput()
	t.Logf("%s", gitversion) // E.g. "git version 2.30.2"

	// Serve all repositories under root using the Smart HTTP protocol so
	// they can be cloned as we explicitly disable the file protocol.
	// (Another option would be to use `git daemon` and the Git protocol,
	// but that listens on a fixed port number which is a recipe for
	// disaster in CI. Funnily enough, `git daemon --port=0` works but there
	// is no easy way to discover which port got picked!)

	githttp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var logs bytes.Buffer
		(&cgi.Handler{
			Path: gitpath,
			Args: []string{"http-backend"},
			Dir:  root,
			Env: []string{
				"GIT_PROJECT_ROOT=" + root,
				"GIT_HTTP_EXPORT_ALL=1",
			},
			Stderr: &logs,
		}).ServeHTTP(w, r)
		if logs.Len() == 0 {
			return
		}
		for {
			line, err := logs.ReadString('\n')
			t.Log("git-http-backend: " + line)
			if err != nil {
				break
			}
		}
	})
	server := httptest.NewServer(&githttp)
	t.Cleanup(server.Close)
	return server.URL
}

func runShell(t *testing.T, dir string, cmds ...string) {
	t.Helper()
	for _, args := range cmds {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell", "-command", args)
		} else {
			cmd = exec.Command("sh", "-c", args)
		}
		cmd.Dir = dir
		// cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		require.NoErrorf(t, cmd.Run(), "error running %v", args)
	}
}

func logProgressStreams(ctx context.Context, t *testing.T) context.Context {
	pr, ctx, cancel := progress.NewContext(ctx)
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel(errors.WithStack(context.Canceled))
		<-done
	})
	go func() {
		defer close(done)
		for {
			prog, err := pr.Read(context.Background())
			if err != nil {
				return
			}
			for _, log := range prog {
				switch lsys := log.Sys.(type) {
				case client.VertexLog:
					var stream string
					switch lsys.Stream {
					case 1:
						stream = "stdout"
					case 2:
						stream = "stderr"
					default:
						stream = strconv.FormatInt(int64(lsys.Stream), 10)
					}
					t.Logf("(%v) %s", stream, lsys.Data)
				default:
					t.Logf("(%T) %+v", log.Sys, log)
				}
			}
		}
	}()
	return ctx
}
