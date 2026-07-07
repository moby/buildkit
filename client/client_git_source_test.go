package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/content/local"
	intoto "github.com/in-toto/in-toto-golang/in_toto"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/gitutil/gitobject"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// testGitBundleRoundTrip exercises the bundle round-trip end to end:
//
//  1. Seed a git repo served over HTTP.
//  2. Build 1 uses GitCheckoutBundle() to export a single-file git bundle.
//  3. Build 2 imports that bundle via GitBundleURL("oci-layout+blob://...")
//     against a client-side OCI layout store, once with the sha pinned as ref
//     and once with a symbolic ref ("master"). Both checkouts must succeed
//     and produce a worktree matching Build 0 (plain git clone).
//  4. A KeepGitDir subtest exercises llb.KeepGitDir() + bundle and asserts
//     no fake refs/buildkit/* namespace leaks into the checkout's .git dir.
//  5. A provenance subtest enables attest:provenance and asserts both the
//     git-commit material and the bundle-blob material are recorded.
func testGitBundleRoundTrip(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCILayout)
	integration.SkipOnPlatform(t, "windows")
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	gitDir := t.TempDir()
	err = runInDir(gitDir,
		"git init -b master",
		"git config --local user.email test@example.com",
		"git config --local user.name test",
		"echo hello > file.txt",
		"git add file.txt",
		"git commit -m initial",
		"git update-server-info",
	)
	require.NoError(t, err)

	cmd := exec.CommandContext(context.TODO(), "git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	out, err := cmd.Output()
	require.NoError(t, err)
	headSha := strings.TrimSpace(string(out))

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()
	repoURL := server.URL + "/.git"

	// Build 1: export a bundle via GitCheckoutBundle.
	bundleDir := t.TempDir()
	st := llb.Git(repoURL, "master", llb.GitChecksum(headSha), llb.GitCheckoutBundle())
	def, err := st.Marshal(ctx)
	require.NoError(t, err)

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: bundleDir}},
	}, nil)
	require.NoError(t, err)

	bundleBytes, err := os.ReadFile(filepath.Join(bundleDir, "bundle"))
	require.NoError(t, err)
	require.NotEmpty(t, bundleBytes)
	bundleDgst := digest.FromBytes(bundleBytes)

	// Sanity-check the bundle locally: it must contain the commit we pinned,
	// landing in the natural ref namespace (refs/heads/master).
	localBare := t.TempDir()
	err = runInDir(localBare, "git init --bare")
	require.NoError(t, err)
	bundleRoot := t.TempDir()
	root, err := os.OpenRoot(bundleRoot)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile("bundle.pack", bundleBytes, 0644))
	require.NoError(t, root.Close())
	bundlePath := filepath.Join(bundleRoot, "bundle.pack")
	err = runInDir(localBare, fmt.Sprintf("git fetch %s +refs/*:refs/*", bundlePath))
	require.NoError(t, err)
	//nolint:gosec // Test-controlled temp dir and commit SHA are not attacker input.
	cmd = exec.CommandContext(context.TODO(), "git", "--git-dir="+localBare, "cat-file", "-e", headSha+"^{commit}")
	require.NoError(t, cmd.Run(), "bundle does not contain expected commit")
	//nolint:gosec // Test-controlled temp dir is not attacker input.
	cmd = exec.CommandContext(context.TODO(), "git", "--git-dir="+localBare, "rev-parse", "--verify", "refs/heads/master")
	masterOut, err := cmd.CombinedOutput()
	require.NoError(t, err, "bundle should carry user's ref refs/heads/master; output=%q", string(masterOut))
	require.Equal(t, headSha, strings.TrimSpace(string(masterOut)))

	// Stage the bundle in a client-side OCI layout store so Build 2 can
	// resolve it through an oci-layout+blob:// locator.
	storeDir := t.TempDir()
	store, err := local.NewStore(storeDir)
	require.NoError(t, err)
	err = content.WriteBlob(ctx, store, "bundle-"+bundleDgst.String(), bytes.NewReader(bundleBytes),
		ocispecs.Descriptor{Digest: bundleDgst, Size: int64(len(bundleBytes))})
	require.NoError(t, err)

	csID := "git-bundle-oci-store"
	bundleLocator := fmt.Sprintf("oci-layout+blob://not/real@%s", bundleDgst.String())

	cases := []struct {
		name string
		ref  string // value passed as the fragment/ref to llb.Git
	}{
		{"sha-ref", headSha},
		{"symbolic-ref", "master"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			destDir := t.TempDir()
			st := llb.Git(
				repoURL,
				tc.ref,
				llb.GitChecksum(headSha),
				llb.GitBundleURL(bundleLocator, llb.GitBundleOCIStore("", csID)),
			)
			def, err := st.Marshal(sb.Context())
			require.NoError(t, err)

			_, err = c.Solve(sb.Context(), def, SolveOpt{
				Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: destDir}},
				OCIStores: map[string]content.Store{
					csID: store,
				},
			}, nil)
			require.NoError(t, err)

			dt, err := os.ReadFile(filepath.Join(destDir, "file.txt"))
			require.NoError(t, err)
			require.Equal(t, "hello\n", string(dt))
		})
	}

	// KeepGitDir + bundle: the checkout's .git must not contain any leaked
	// refs/buildkit/* namespace. `.git/refs/heads/*` and `.git/refs/tags/*`
	// are populated by the plain-git checkout path (it runs its own fetch
	// into a freshly inited repo), so we only assert no refs/buildkit/
	// leak — the whole point of Item A is that the bundle no longer uses
	// that namespace.
	t.Run("keepgitdir-no-namespace-leak", func(t *testing.T) {
		destDir := t.TempDir()
		st := llb.Git(
			repoURL,
			"master",
			llb.GitChecksum(headSha),
			llb.GitBundleURL(bundleLocator, llb.GitBundleOCIStore("", csID)),
			llb.KeepGitDir(),
		)
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: destDir}},
			OCIStores: map[string]content.Store{
				csID: store,
			},
		}, nil)
		require.NoError(t, err)

		// .git dir should be present.
		st2, err := os.Stat(filepath.Join(destDir, ".git"))
		require.NoError(t, err)
		require.True(t, st2.IsDir(), ".git should be a directory")

		// No fake internal namespace anywhere under the checkout's .git.
		leakRoot := filepath.Join(destDir, ".git", "refs", "buildkit")
		_, err = os.Stat(leakRoot)
		require.True(t, os.IsNotExist(err), ".git/refs/buildkit should not exist: %v", err)

		// Also scan packed-refs, which is where cloned/fetched refs may
		// live on disk after git gc / pack-refs.
		if pr, err := os.ReadFile(filepath.Join(destDir, ".git", "packed-refs")); err == nil {
			require.NotContains(t, string(pr), "refs/buildkit/",
				"packed-refs should not carry refs/buildkit/* entries")
		}

		// The user's ref name must survive into .git: passing "master" to
		// llb.Git should produce a checkout whose .git carries
		// refs/heads/master, either loose or packed.
		//nolint:gosec // Test-controlled export dir is not attacker input.
		cmd := exec.CommandContext(context.TODO(), "git",
			"--git-dir="+filepath.Join(destDir, ".git"),
			"rev-parse", "--verify", "refs/heads/master")
		refOut, err := cmd.CombinedOutput()
		require.NoError(t, err, ".git should carry refs/heads/master; output=%q", string(refOut))
		require.Equal(t, headSha, strings.TrimSpace(string(refOut)))

		// HEAD must point at the pinned commit.
		//nolint:gosec // Test-controlled export dir is not attacker input.
		cmd = exec.CommandContext(context.TODO(), "git",
			"--git-dir="+filepath.Join(destDir, ".git"),
			"rev-parse", "HEAD")
		gotOut, err := cmd.CombinedOutput()
		require.NoError(t, err, "output=%q", string(gotOut))
		require.Equal(t, headSha, strings.TrimSpace(string(gotOut)))

		// file.txt content matches.
		dt, err := os.ReadFile(filepath.Join(destDir, "file.txt"))
		require.NoError(t, err)
		require.Equal(t, "hello\n", string(dt))
	})

	// Provenance subtest: assert that the git-commit material and the
	// bundle-blob material are both recorded.
	t.Run("provenance", func(t *testing.T) {
		workers.CheckFeatureCompat(t, sb, workers.FeatureProvenance)
		destDir := t.TempDir()
		st := llb.Scratch().File(
			llb.Copy(
				llb.Git(
					repoURL,
					"master",
					llb.GitChecksum(headSha),
					llb.GitBundleURL(bundleLocator, llb.GitBundleOCIStore("", csID)),
				),
				"file.txt",
				"file.txt",
			),
		)
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{
			FrontendAttrs: map[string]string{
				"attest:provenance": "",
			},
			Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: destDir}},
			OCIStores: map[string]content.Store{
				csID: store,
			},
		}, nil)
		require.NoError(t, err)

		provDt, err := os.ReadFile(filepath.Join(destDir, "provenance.json"))
		require.NoError(t, err)

		var stmt struct {
			intoto.StatementHeader
			Predicate provenancetypes.ProvenancePredicateSLSA1 `json:"predicate"`
		}
		require.NoError(t, json.Unmarshal(provDt, &stmt))

		// Bundle-backed git sources emit two materials: the git source
		// at its raw repository URL (same shape as a non-bundle git
		// source), and the bundle blob as a purl with a vcs_url
		// qualifier linking back to the git URL.
		expectedGitURI := repoURL + "#master"
		var foundCommit, foundBundle bool
		for _, m := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
			if m.URI == expectedGitURI && m.Digest["sha1"] == headSha {
				foundCommit = true
			}
			if strings.HasPrefix(m.URI, "pkg:oci/") && strings.Contains(m.URI, "ref_type=bundle") && strings.Contains(m.URI, "vcs_url=") && m.Digest["sha256"] == bundleDgst.Hex() {
				foundBundle = true
			}
		}
		require.True(t, foundCommit, "expected git commit material %q with sha1=%s in %+v", expectedGitURI, headSha, stmt.Predicate.BuildDefinition.ResolvedDependencies)
		require.True(t, foundBundle, "expected bundle blob material (pkg:oci/...?ref_type=bundle) with sha256=%s in %+v", bundleDgst.Hex(), stmt.Predicate.BuildDefinition.ResolvedDependencies)
	})

	checkAllReleasable(t, c, sb, false)
}

// testGitBundleRoundTripRegistry mirrors testGitBundleRoundTrip but resolves
// the bundle blob through a registry using the docker-image+blob:// scheme.
// The bundle bytes are uploaded directly to the sandbox registry by digest
// and then re-imported in Build 2.
func testGitBundleRoundTripRegistry(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	integration.SkipOnPlatform(t, "windows")
	requiresLinux(t)
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	gitDir := t.TempDir()
	err = runInDir(gitDir,
		"git init -b master",
		"git config --local user.email test@example.com",
		"git config --local user.name test",
		"echo hello > file.txt",
		"git add file.txt",
		"git commit -m initial",
		"git update-server-info",
	)
	require.NoError(t, err)

	cmd := exec.CommandContext(context.TODO(), "git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	out, err := cmd.Output()
	require.NoError(t, err)
	headSha := strings.TrimSpace(string(out))

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()
	repoURL := server.URL + "/.git"

	// Build 1: export a bundle via GitCheckoutBundle.
	bundleDir := t.TempDir()
	st := llb.Git(repoURL, "master", llb.GitChecksum(headSha), llb.GitCheckoutBundle())
	def, err := st.Marshal(ctx)
	require.NoError(t, err)

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: bundleDir}},
	}, nil)
	require.NoError(t, err)

	bundleBytes, err := os.ReadFile(filepath.Join(bundleDir, "bundle"))
	require.NoError(t, err)
	require.NotEmpty(t, bundleBytes)
	bundleDgst := digest.FromBytes(bundleBytes)

	// Push the bundle as a raw blob into the sandbox registry under a
	// repository name. The docker registry accepts arbitrary blobs by
	// digest; the repository exists implicitly once any blob is uploaded
	// under its name.
	bundleRepoRef := registry + "/foo/bundle@" + bundleDgst.String()
	ingester, err := contentutil.IngesterFromRef(bundleRepoRef)
	require.NoError(t, err)
	err = content.WriteBlob(ctx, ingester, "bundle-"+bundleDgst.String(), bytes.NewReader(bundleBytes),
		ocispecs.Descriptor{Digest: bundleDgst, Size: int64(len(bundleBytes))})
	require.NoError(t, err)

	bundleLocator := "docker-image+blob://" + registry + "/foo/bundle@" + bundleDgst.String()

	t.Run("checkout", func(t *testing.T) {
		destDir := t.TempDir()
		st := llb.Git(
			repoURL,
			"master",
			llb.GitChecksum(headSha),
			llb.GitBundleURL(bundleLocator),
		)
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: destDir}},
		}, nil)
		require.NoError(t, err)

		dt, err := os.ReadFile(filepath.Join(destDir, "file.txt"))
		require.NoError(t, err)
		require.Equal(t, "hello\n", string(dt))
	})

	t.Run("provenance", func(t *testing.T) {
		workers.CheckFeatureCompat(t, sb, workers.FeatureProvenance)
		destDir := t.TempDir()
		st := llb.Scratch().File(
			llb.Copy(
				llb.Git(
					repoURL,
					"master",
					llb.GitChecksum(headSha),
					llb.GitBundleURL(bundleLocator),
				),
				"file.txt",
				"file.txt",
			),
		)
		def, err := st.Marshal(sb.Context())
		require.NoError(t, err)

		_, err = c.Solve(sb.Context(), def, SolveOpt{
			FrontendAttrs: map[string]string{
				"attest:provenance": "",
			},
			Exports: []ExportEntry{{Type: ExporterLocal, OutputDir: destDir}},
		}, nil)
		require.NoError(t, err)

		provDt, err := os.ReadFile(filepath.Join(destDir, "provenance.json"))
		require.NoError(t, err)

		var stmt struct {
			intoto.StatementHeader
			Predicate provenancetypes.ProvenancePredicateSLSA1 `json:"predicate"`
		}
		require.NoError(t, json.Unmarshal(provDt, &stmt))

		expectedGitURI := repoURL + "#master"
		var foundCommit, foundBundle bool
		for _, m := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
			if m.URI == expectedGitURI && m.Digest["sha1"] == headSha {
				foundCommit = true
			}
			if strings.HasPrefix(m.URI, "pkg:docker/") && strings.Contains(m.URI, "ref_type=bundle") && strings.Contains(m.URI, "vcs_url=") && m.Digest["sha256"] == bundleDgst.Hex() {
				foundBundle = true
			}
		}
		require.True(t, foundCommit, "expected git commit material %q with sha1=%s in %+v", expectedGitURI, headSha, stmt.Predicate.BuildDefinition.ResolvedDependencies)
		require.True(t, foundBundle, "expected bundle blob material (pkg:docker/...?ref_type=bundle) with sha256=%s in %+v", bundleDgst.Hex(), stmt.Predicate.BuildDefinition.ResolvedDependencies)
	})

	checkAllReleasable(t, c, sb, false)
}

func testGitResolveMutatedSource(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	gitDir := t.TempDir()
	gitCommands := []string{
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"echo a > a",
		"git add a",
		"git commit -m a",
		"git tag -a v0.1 -m v0.1",
		"echo b > b",
		"git add b",
		"git commit -m b",
		"git checkout -B v2",
		"git update-server-info",
	}
	err = runInDir(gitDir, gitCommands...)
	require.NoError(t, err)

	cmd := exec.CommandContext(context.TODO(), "git", "rev-parse", "v0.1")
	cmd.Dir = gitDir
	out, err := cmd.Output()
	require.NoError(t, err)
	commitTag := strings.TrimSpace(string(out))

	cmd = exec.CommandContext(context.TODO(), "git", "rev-parse", "v0.1^{commit}")
	cmd.Dir = gitDir
	out, err = cmd.Output()
	require.NoError(t, err)
	commitTagCommit := strings.TrimSpace(string(out))

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()

	dest := t.TempDir()

	_, err = c.Build(ctx, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: dest,
			},
		},
	}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		id := "git://" + strings.TrimPrefix(server.URL, "http://") + "/.git#v0.1"
		md, err := c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
			Attrs: map[string]string{
				"git.fullurl": server.URL + "/.git",
			},
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.Git)
		require.Equal(t, "refs/tags/v0.1", md.Git.Ref)
		require.Equal(t, commitTag, md.Git.Checksum)
		require.Equal(t, commitTagCommit, md.Git.CommitChecksum)
		require.Equal(t, id, md.Op.Identifier)
		require.Equal(t, server.URL+"/.git", md.Op.Attrs["git.fullurl"])

		// update the tag to point to a different commit
		err = runInDir(gitDir, []string{
			"git tag -f v0.1",
			"git update-server-info",
		}...)
		require.NoError(t, err)

		md, err = c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
			Attrs: map[string]string{
				"git.fullurl": server.URL + "/.git",
			},
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.Git)
		require.Equal(t, "refs/tags/v0.1", md.Git.Ref)
		require.Equal(t, commitTag, md.Git.Checksum)
		require.Equal(t, commitTagCommit, md.Git.CommitChecksum)
		require.Equal(t, id, md.Op.Identifier)
		require.Equal(t, server.URL+"/.git", md.Op.Attrs["git.fullurl"])

		st := llb.Git(server.URL+"/.git", "", llb.GitRef("v0.1"))
		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}
		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}, nil)
	require.NoError(t, err)

	_, err = os.ReadFile(filepath.Join(dest, "b"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err), "expected file b to not exist")

	dt, err := os.ReadFile(filepath.Join(dest, "a"))
	require.NoError(t, err)
	require.Equal(t, "a\n", string(dt))

	checkAllReleasable(t, c, sb, false)
}

func testGitResolveSourceMetadata(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()
	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	gitDir := t.TempDir()
	gitCommands := []string{
		"git init",
		"git config --local user.email test@example.com",
		"git config --local user.name test",
		"touch a",
		"git add a",
		"git commit -m msg",
		"git tag -a v0.1 -m v0.1release",
		"echo b > b",
		"git add b",
		"git commit -m b",
		"git checkout -B v2",
		"git update-server-info",
	}
	err = runInDir(gitDir, gitCommands...)
	require.NoError(t, err)

	cmd := exec.CommandContext(context.TODO(), "git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	out, err := cmd.Output()
	require.NoError(t, err)
	commitHEAD := strings.TrimSpace(string(out))

	cmd = exec.CommandContext(context.TODO(), "git", "rev-parse", "v0.1")
	cmd.Dir = gitDir
	out, err = cmd.Output()
	require.NoError(t, err)
	commitTag := strings.TrimSpace(string(out))

	cmd = exec.CommandContext(context.TODO(), "git", "rev-parse", "v0.1^{commit}")
	cmd.Dir = gitDir
	out, err = cmd.Output()
	require.NoError(t, err)
	commitTagCommit := strings.TrimSpace(string(out))

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()

	_, err = c.Build(ctx, SolveOpt{}, "test", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		id := "git://" + strings.TrimPrefix(server.URL, "http://") + "/.git"
		md, err := c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
			Attrs: map[string]string{
				"git.fullurl": server.URL + "/.git",
			},
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.Git)
		require.Equal(t, "refs/heads/v2", md.Git.Ref) // default to branch head
		require.Equal(t, commitHEAD, md.Git.Checksum)
		require.Equal(t, "", md.Git.CommitChecksum) // not annotated tag
		require.Equal(t, id, md.Op.Identifier)
		require.Equal(t, server.URL+"/.git", md.Op.Attrs["git.fullurl"])
		require.Nil(t, md.Git.CommitObject)
		require.Nil(t, md.Git.TagObject)

		id += "#v0.1"
		md, err = c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
			Attrs: map[string]string{
				"git.fullurl": server.URL + "/.git",
			},
		}, sourceresolver.Opt{})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.Git)
		require.Equal(t, "refs/tags/v0.1", md.Git.Ref)
		require.Equal(t, commitTag, md.Git.Checksum) // annotated tag
		require.Equal(t, commitTagCommit, md.Git.CommitChecksum)

		require.Equal(t, id, md.Op.Identifier)
		require.Equal(t, server.URL+"/.git", md.Op.Attrs["git.fullurl"])
		require.Nil(t, md.Git.CommitObject)
		require.Nil(t, md.Git.TagObject)

		md, err = c.ResolveSourceMetadata(ctx, &pb.SourceOp{
			Identifier: id,
			Attrs: map[string]string{
				"git.fullurl": server.URL + "/.git",
			},
		}, sourceresolver.Opt{
			GitOpt: &sourceresolver.ResolveGitOpt{
				ReturnObject: true,
			},
		})
		if err != nil {
			return nil, err
		}
		require.NotNil(t, md.Git)
		require.Equal(t, "refs/tags/v0.1", md.Git.Ref)
		require.Equal(t, commitTag, md.Git.Checksum) // annotated tag
		require.Equal(t, commitTagCommit, md.Git.CommitChecksum)

		require.Equal(t, id, md.Op.Identifier)
		require.Equal(t, server.URL+"/.git", md.Op.Attrs["git.fullurl"])
		require.NotNil(t, md.Git.CommitObject)
		require.NotNil(t, md.Git.TagObject)

		commitObj, err := gitobject.Parse(md.Git.CommitObject)
		require.NoError(t, err)
		require.NoError(t, commitObj.VerifyChecksum(md.Git.CommitChecksum))

		commit, err := commitObj.ToCommit()
		require.NoError(t, err)
		require.Equal(t, "msg", commit.Message)
		require.Equal(t, "test", commit.Author.Name)
		require.Equal(t, "test@example.com", commit.Author.Email)
		require.Equal(t, "test", commit.Committer.Name)
		require.Equal(t, "test@example.com", commit.Committer.Email)
		commitTime := commit.Committer.When
		require.NotNil(t, commitTime)
		require.WithinDuration(t, time.Now(), *commitTime, 2*time.Minute)

		tagObj, err := gitobject.Parse(md.Git.TagObject)
		require.NoError(t, err)
		require.NoError(t, tagObj.VerifyChecksum(md.Git.Checksum))

		tag, err := tagObj.ToTag()
		require.NoError(t, err)
		require.Equal(t, "v0.1release", tag.Message)
		require.Equal(t, "v0.1", tag.Tag)
		require.Equal(t, "test", tag.Tagger.Name)
		require.Equal(t, "test@example.com", tag.Tagger.Email)
		tagTime := tag.Tagger.When
		require.NotNil(t, tagTime)
		require.WithinDuration(t, time.Now(), *tagTime, 2*time.Minute)
		return nil, nil
	}, nil)
	require.NoError(t, err)
}

func runInDir(dir string, cmds ...string) error {
	return runInDirEnv(dir, nil, cmds...)
}

func runInDirEnv(dir string, env []string, cmds ...string) error {
	for _, args := range cmds {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(context.TODO(), "powershell", "-command", args)
		} else {
			cmd = exec.CommandContext(context.TODO(), "sh", "-c", args)
		}
		cmd.Env = append(os.Environ(), env...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "error running %v", args)
		}
	}
	return nil
}
