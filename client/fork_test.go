package client

// Integration tests for the fork's main features:
//   - eager-export (compress + push modes), multi-stage, shared base, force-compression,
//     concurrent solves (regression test for PR #25), cancellation, push failure,
//     validation errors.
//   - prefer-push-registry: hit, miss (404 fallback), validation errors, combined with
//     eager-export=push.
//
// These tests live alongside the two existing testEagerExport{Compress,Push} cases in
// client_test.go and are registered via init() append to allTests, following the
// pattern used by client_nydus_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func init() {
	allTests = append(allTests,
		testEagerExportPushMultiStage,
		testEagerExportPushSharedBase,
		testEagerExportPushForceCompression,
		testEagerExportPushConcurrentSolves,
		testEagerExportPushBadRegistry,
		testEagerExportPushCancellation,
		testEagerExportCompressNoPushOnRegistry,
		testEagerExportValidation,
		testPreferPushRegistryHit,
		testPreferPushRegistryMiss,
		testPreferPushRegistryPartialHit,
		testPreferPushRegistryWithEagerExport,
		testPreferPushRegistryValidation,
		testParallelExtractWhiteouts,
	)
}

// eagerMultiStageDef builds a two-stage LLB graph:
//
//	stage builder: busybox + /wd/built (from stage1 `RUN`)
//	stage final: busybox + /wd/from-builder (copied from builder) + /wd/final
//
// Exercises eager export on a non-trivial, multi-stage LLB chain where the
// final export ref's chain includes layers produced by a separate stage.
func eagerMultiStageDef(t *testing.T, sb integration.Sandbox) *llb.Definition {
	t.Helper()
	busybox := llb.Image("busybox:latest")

	builder := busybox.
		Run(llb.Shlex(`sh -c "echo built > built"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())

	final := busybox.
		Run(llb.Shlex(`sh -c "echo final > final"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())
	final = final.File(llb.Copy(builder, "built", "/from-builder"))

	def, err := final.Marshal(sb.Context())
	require.NoError(t, err)
	return def
}

// testEagerExportPushMultiStage drives a multi-stage LLB graph through the
// eager push pipeline and verifies the final exported image has both the
// stage-local file and the file copied from the earlier stage.
func testEagerExportPushMultiStage(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testeagermultistage:latest"
	def := eagerMultiStageDef(t, sb)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":         target,
					"push":         "true",
					"eager-export": "push",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	destDir := t.TempDir()
	pullDef, err := llb.Image(target).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "final"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "final")
	dt, err = os.ReadFile(filepath.Join(destDir, "from-builder"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "built")
}

// testEagerExportPushSharedBase builds two images in one solve (via two
// exporter targets) that share a parent chain. The per-digest tracker in
// the push pool must upload each shared blob only once; verified end-to-end
// by pulling both images back and checking their manifests reference an
// overlapping set of layer digests.
func testEagerExportPushSharedBase(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	busybox := llb.Image("busybox:latest")
	base := busybox.
		Run(llb.Shlex(`sh -c "echo shared > shared"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())
	variantA := busybox.
		Run(llb.Shlex(`sh -c "echo a > a"`), llb.Dir("/wd")).
		AddMount("/wd", base)
	variantB := busybox.
		Run(llb.Shlex(`sh -c "echo b > b"`), llb.Dir("/wd")).
		AddMount("/wd", base)

	targetA := registry + "/buildkit/testeagersharedbase-a:latest"
	targetB := registry + "/buildkit/testeagersharedbase-b:latest"

	for _, tc := range []struct {
		target string
		st     llb.State
	}{
		{targetA, variantA},
		{targetB, variantB},
	} {
		def, err := tc.st.Marshal(sb.Context())
		require.NoError(t, err)
		_, err = c.Solve(sb.Context(), def, SolveOpt{
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":         tc.target,
						"push":         "true",
						"eager-export": "push",
					},
				},
			},
		}, nil)
		require.NoError(t, err)
	}

	// Both manifests must resolve and share at least one layer digest (the
	// `shared` base layer). If eager push's per-digest dedup dropped the
	// shared blob on the floor for one of the pushes, the second pull would
	// fail because its manifest references a blob that was never uploaded.
	layersA := readLayerDigests(t, sb, targetA)
	layersB := readLayerDigests(t, sb, targetB)
	require.NotEmpty(t, layersA)
	require.NotEmpty(t, layersB)

	shared := intersectStrings(layersA, layersB)
	require.NotEmpty(t, shared, "shared base layer digest must appear in both manifests")
}

// testEagerExportPushForceCompression verifies eager push composes cleanly
// with force-compression=true. The writer path may need to recompress layers
// at finalize time; eager pre-pushes the pre-forced blobs, so we make sure
// the finalize path still ends with a valid, pullable image.
func testEagerExportPushForceCompression(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	busybox := llb.Image("busybox:latest")
	st := busybox.
		Run(llb.Shlex(`sh -c "echo payload > file"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	target := registry + "/buildkit/testeagerforcecompression:latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              target,
					"push":              "true",
					"eager-export":      "push",
					"compression":       "gzip",
					"force-compression": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	destDir := t.TempDir()
	pullDef, err := llb.Image(target).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err)
	dt, err := os.ReadFile(filepath.Join(destDir, "file"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "payload")
}

// testEagerExportPushConcurrentSolves regression-tests PR #25: two solves
// on the same client with overlapping LLB (so the second Job joins an
// already-resolved sharedOp/edge) must still get every export ref through
// the eager pipeline via the backfill path. Pre-fix, the second build's
// exporter failed with "no blobs for snapshot" on cache-shared refs.
func testEagerExportPushConcurrentSolves(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	busybox := llb.Image("busybox:latest")
	// Shared prefix across all concurrent solves — these vertices are what
	// the second+ builds will dedupe against via the scheduler's sharedOp.
	shared := busybox.
		Run(llb.Shlex(`sh -c "echo s1 > s1 && echo s2 > s2"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())

	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := registryTagName(registry, "testeagerconcurrent", i)
			// Per-solve tail vertex prevents the whole graph from being an
			// exact cache hit in the scheduler, while keeping the shared
			// prefix vertices dedupable.
			final := busybox.
				Run(llb.Shlex("sh -c \"echo "+itoa(i)+" > tail\""), llb.Dir("/wd")).
				AddMount("/wd", shared)
			def, err := final.Marshal(sb.Context())
			if err != nil {
				errs[i] = err
				return
			}
			_, errs[i] = c.Solve(sb.Context(), def, SolveOpt{
				Exports: []ExportEntry{
					{
						Type: ExporterImage,
						Attrs: map[string]string{
							"name":         target,
							"push":         "true",
							"eager-export": "push",
						},
					},
				},
			}, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "concurrent solve #%d failed", i)
	}

	// Every pushed image must be pullable — this is the direct regression
	// check: pre-fix, at least one of the concurrent builds would push a
	// manifest referencing a blob that was never uploaded, so its pull-back
	// would 404.
	for i := range n {
		target := registryTagName(registry, "testeagerconcurrent", i)
		destDir := t.TempDir()
		pullDef, err := llb.Image(target).Marshal(sb.Context())
		require.NoError(t, err)
		_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
			Exports: []ExportEntry{
				{Type: ExporterLocal, OutputDir: destDir},
			},
		}, nil)
		require.NoError(t, err, "concurrent solve #%d pull-back failed", i)

		dt, err := os.ReadFile(filepath.Join(destDir, "s1"))
		require.NoError(t, err)
		require.Contains(t, string(dt), "s1")
		dt, err = os.ReadFile(filepath.Join(destDir, "tail"))
		require.NoError(t, err)
		require.Contains(t, string(dt), itoa(i))
	}
}

// testEagerExportPushBadRegistry verifies push errors from the eager pool
// propagate out through wait() rather than being swallowed. Uses an
// unroutable host:port so the push fails deterministically.
func testEagerExportPushBadRegistry(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.
		Run(llb.Shlex(`sh -c "echo x > file"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	// 127.0.0.1:1 is reserved/unbound on all common setups; connections
	// fail fast with connection-refused.
	target := "127.0.0.1:1/buildkit/testeagerbadregistry:latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":         target,
					"push":         "true",
					"eager-export": "push",
					"registry.insecure": "true",
				},
			},
		},
	}, nil)
	require.Error(t, err)
}

// testEagerExportPushCancellation cancels a solve mid-flight while the
// eager pipeline is actively compressing and pushing. The daemon must not
// panic and the same client must be able to run a fresh solve right after.
// Regression sentinel for the send-on-closed-channel class of bug (PR #18).
func testEagerExportPushCancellation(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	busybox := llb.Image("busybox:latest")
	// Deliberately slow: a 3s sleep gives us a predictable window to
	// cancel while compress / push workers are spun up.
	slow := busybox.
		Run(llb.Shlex(`sh -c "echo a > a && sleep 3 && echo b > b"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())
	def, err := slow.Marshal(sb.Context())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(sb.Context())
	go func() {
		time.Sleep(750 * time.Millisecond)
		cancel()
	}()
	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":         registry + "/buildkit/testeagercancel:latest",
					"push":         "true",
					"eager-export": "push",
				},
			},
		},
	}, nil)
	require.Error(t, err) // cancelled

	// Sanity: the daemon is still alive and a fresh build still works.
	followupDef, err := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c "echo ok > ok"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch()).Marshal(sb.Context())
	require.NoError(t, err)
	destDir := t.TempDir()
	_, err = c.Solve(sb.Context(), followupDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err, "daemon must still accept builds after eager push cancellation")
}

// testEagerExportCompressNoPushOnRegistry verifies eager-export=compress
// does not reach out to the registry: we point at an unroutable host with
// push=false. If compress mode leaked into the push path, NewPusher would
// fail fast with a connection error.
func testEagerExportCompressNoPushOnRegistry(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := busybox.
		Run(llb.Shlex(`sh -c "echo x > file"`), llb.Dir("/wd")).
		AddMount("/wd", llb.Scratch())
	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":         "127.0.0.1:1/shouldnotbepushed:latest",
					"push":         "false",
					"eager-export": "compress",
				},
			},
		},
	}, nil)
	require.NoError(t, err, "compress mode with push=false must not touch the registry")
}

// testEagerExportValidation covers the validate/error branches in
// control/control.go that aren't hit by the happy-path tests.
func testEagerExportValidation(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	trivial := llb.Scratch().File(llb.Mkfile("x", 0600, []byte("x")))
	def, err := trivial.Marshal(sb.Context())
	require.NoError(t, err)

	cases := []struct {
		name        string
		exports     []ExportEntry
		errContains string
	}{
		{
			name: "push mode without push=true",
			exports: []ExportEntry{{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":         registry + "/buildkit/testeagerval1:latest",
					"push":         "false",
					"eager-export": "push",
				},
			}},
			errContains: "push",
		},
		{
			name: "unknown eager-export value",
			exports: []ExportEntry{{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":         registry + "/buildkit/testeagerval2:latest",
					"push":         "true",
					"eager-export": "bogus",
				},
			}},
			errContains: "eager-export",
		},
		{
			name: "wildcard name",
			exports: []ExportEntry{{
				Type: ExporterImage,
				Attrs: map[string]string{
					// Bare `*` is the sentinel EagerPushConfig rejects.
					"name":         "*",
					"push":         "true",
					"eager-export": "push",
				},
			}},
			errContains: "name",
		},
		{
			name: "two image exporters with eager push",
			exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":         registry + "/buildkit/testeagerval4a:latest",
						"push":         "true",
						"eager-export": "push",
					},
				},
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":         registry + "/buildkit/testeagerval4b:latest",
						"push":         "true",
						"eager-export": "push",
					},
				},
			},
			errContains: "exactly one",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Solve(sb.Context(), def, SolveOpt{Exports: tc.exports}, nil)
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.errContains))
		})
	}
}

// seedBaseImage builds and pushes a base image with busybox's rootfs plus
// a marker file, so it's both (a) pullable/introspectable and (b) usable as
// the source for a child RUN step (the child needs sh+coreutils from
// busybox). Returns the target ref.
func seedBaseImage(t *testing.T, c *Client, sb integration.Sandbox, target string) {
	t.Helper()
	def, err := llb.Image("busybox:latest").
		File(llb.Mkfile("/base-file", 0644, []byte("baseline"))).
		Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)
}

// testPreferPushRegistryHit: seed the push registry's child repo with a
// base image, then build a child that FROMs it and exports with
// prefer-push-registry=true to the **same repo** (different tag). At
// probe time, the registry already has every base layer blob in that
// repo, so every probe hits and every layer is served via
// pushFallbackProvider.
//
// Note the repo paths matter: the prober scopes `/v2/<repo>/blobs/<digest>`
// HEADs to `cfg.TargetName`'s repo. Different-repo seeding (which we
// originally tried) ends up exercising the miss path because the standard
// distribution registry tracks blob membership per-repo.
func testPreferPushRegistryHit(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	// Use the same repo path on the registry for both seed and child so
	// that pushing the base image populates the child's blob namespace.
	baseTarget := registry + "/buildkit/pushreghit:base"
	childTarget := registry + "/buildkit/pushreghit:child"
	seedBaseImage(t, c, sb, baseTarget)

	child := llb.Image(baseTarget).
		Run(llb.Shlex(`sh -c "echo child > /child-file"`)).Root()
	childDef, err := child.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), childDef, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                 childTarget,
					"push":                 "true",
					"prefer-push-registry": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	destDir := t.TempDir()
	pullDef, err := llb.Image(childTarget).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err)
	dt, err := os.ReadFile(filepath.Join(destDir, "child-file"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "child")
	dt, err = os.ReadFile(filepath.Join(destDir, "base-file"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "baseline")
}

// testPreferPushRegistryPartialHit: pre-seed the push registry's child
// repo with a *prefix* of the source's layer chain, so per-descriptor
// probes split — some layers hit the push registry, others 404 and fall
// back to origin. Exercises the per-descriptor decision logic in
// pushFallbackProvider.ReaderAt across a real manifest.
//
// Layout:
//
//	originReg  – stable: hosts the full source image (busybox + /file-a + /file-b)
//	pushReg    – partial: hosts only `busybox + /file-a` under the same repo
//	             path the child will export to, so the busybox and /file-a
//	             layer digests appear there but the /file-b layer digest does not.
func testPreferPushRegistryPartialHit(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	originReg, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	pushReg, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	// Same repo path on both registries so layer digests line up across
	// pulls/pushes; tags differentiate the partial-prefix vs. full-chain
	// seeds.
	const repo = "/buildkit/pushregpartial"
	baseRefOrigin := originReg + repo + ":base"
	baseRefPush := pushReg + repo + ":base"
	extendedRef := originReg + repo + ":extended"
	childRef := pushReg + repo + ":child"

	// Step 1: build base = busybox + /file-a, push to BOTH origin and push
	// registries. After this, pushReg's repo has the busybox and /file-a
	// layer blobs (under the :base tag).
	baseDef, err := llb.Image("busybox:latest").
		File(llb.Mkfile("/file-a", 0644, []byte("a"))).
		Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), baseDef, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": baseRefOrigin + "," + baseRefPush,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Step 2: build extended = base + /file-b, push to ORIGIN only. This
	// is the source we'll FROM. Its layer chain is busybox + /file-a +
	// /file-b — the first two have the same digests as the partial seed
	// in pushReg; the third is unique to origin.
	extendedDef, err := llb.Image(baseRefOrigin).
		File(llb.Mkfile("/file-b", 0644, []byte("b"))).
		Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), extendedDef, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{"name": extendedRef, "push": "true"},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Force the next solve to actually pull (and probe) the source layers
	// instead of taking everything from local cache.
	ensurePruneAll(t, c, sb)

	// Step 3: child FROMs the extended image and exports to pushReg with
	// prefer-push-registry=true. During the source pull, the per-layer
	// probe asks pushReg's child repo for each layer digest:
	//   - busybox layer    → HIT  (seeded under :base)
	//   - /file-a layer    → HIT  (seeded under :base)
	//   - /file-b layer    → MISS → fallback to origin
	child := llb.Image(extendedRef).
		Run(llb.Shlex(`sh -c "echo child > /child-file"`)).Root()
	childDef, err := child.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), childDef, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                 childRef,
					"push":                 "true",
					"prefer-push-registry": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err, "partial-hit must transparently fall back to origin for missing layers")

	// Verify pull-back contains all three pre-existing files plus the new
	// /child-file: every layer was sourced correctly regardless of
	// whether it came from pushReg or originReg.
	destDir := t.TempDir()
	pullDef, err := llb.Image(childRef).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err)
	for _, f := range []struct{ path, want string }{
		{"file-a", "a"},
		{"file-b", "b"},
		{"child-file", "child"},
	} {
		dt, err := os.ReadFile(filepath.Join(destDir, f.path))
		require.NoError(t, err, "missing %s after partial-hit pull-back", f.path)
		require.Contains(t, string(dt), f.want, "wrong content in %s", f.path)
	}

	// Behavioural sanity: scan buildkitd logs for the per-layer decision
	// lines. The wrapper logs at info-level for every layer it considers,
	// and we want both at least one hit (`pulled from push registry`) and
	// at least one fallback (`falling back to origin`) for this test to
	// have exercised what its name claims. Don't require strict counts —
	// resolver caching can shift them — just non-zero on each side.
	var hits, fallbacks int
	for _, buf := range sb.Logs() {
		s := buf.String()
		hits += strings.Count(s, "pulled from push registry")
		fallbacks += strings.Count(s, "falling back to origin")
	}
	require.Greater(t, hits, 0, "expected at least one push-registry hit; wrapper did not run or every probe missed")
	require.Greater(t, fallbacks, 0, "expected at least one fallback; the missing layer probe did not 404")
}

// testPreferPushRegistryMiss: the push registry doesn't have the base
// image's layers (different registry from the one hosting the base), so
// the probe 404s and the provider falls back to origin. Build must still
// succeed.
func testPreferPushRegistryMiss(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	originReg, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	pushReg, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	baseTarget := originReg + "/buildkit/pushregmiss-base:latest"
	seedBaseImage(t, c, sb, baseTarget)

	child := llb.Image(baseTarget).
		Run(llb.Shlex(`sh -c "echo child > /child-file"`)).Root()
	childDef, err := child.Marshal(sb.Context())
	require.NoError(t, err)

	childTarget := pushReg + "/buildkit/pushregmiss-child:latest"
	_, err = c.Solve(sb.Context(), childDef, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                 childTarget,
					"push":                 "true",
					"prefer-push-registry": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err, "miss path must transparently fall back to origin")

	destDir := t.TempDir()
	pullDef, err := llb.Image(childTarget).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err)
	dt, err := os.ReadFile(filepath.Join(destDir, "child-file"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "child")
}

// testPreferPushRegistryWithEagerExport exercises the real-world combo:
// prefer-push-registry=true (skip origin where possible) + eager-export=push
// (compress/push during build). Tests that the two flags thread together
// cleanly through control.go / solver.go / source/containerimage/pull.go.
func testPreferPushRegistryWithEagerExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	baseTarget := registry + "/buildkit/pushreg-combo-base:latest"
	seedBaseImage(t, c, sb, baseTarget)

	child := llb.Image(baseTarget).
		Run(llb.Shlex(`sh -c "echo combo > /combo-file"`)).Root()
	childDef, err := child.Marshal(sb.Context())
	require.NoError(t, err)

	childTarget := registry + "/buildkit/pushreg-combo-child:latest"
	_, err = c.Solve(sb.Context(), childDef, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                 childTarget,
					"push":                 "true",
					"prefer-push-registry": "true",
					"eager-export":         "push",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	destDir := t.TempDir()
	pullDef, err := llb.Image(childTarget).Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err)
	dt, err := os.ReadFile(filepath.Join(destDir, "combo-file"))
	require.NoError(t, err)
	require.Contains(t, string(dt), "combo")
}

// testPreferPushRegistryValidation covers the validate/error branches for
// prefer-push-registry: requires push=true, requires single image name,
// rejected on multiple image exporters, rejected on non-image exporter.
func testPreferPushRegistryValidation(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	trivial := llb.Scratch().File(llb.Mkfile("x", 0600, []byte("x")))
	def, err := trivial.Marshal(sb.Context())
	require.NoError(t, err)

	cases := []struct {
		name        string
		exports     []ExportEntry
		errContains string
	}{
		{
			name: "requires push=true",
			exports: []ExportEntry{{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                 registry + "/buildkit/pushregval1:latest",
					"push":                 "false",
					"prefer-push-registry": "true",
				},
			}},
			errContains: "push",
		},
		{
			name: "two image exporters with prefer-push-registry",
			exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":                 registry + "/buildkit/pushregval2a:latest",
						"push":                 "true",
						"prefer-push-registry": "true",
					},
				},
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name":                 registry + "/buildkit/pushregval2b:latest",
						"push":                 "true",
						"prefer-push-registry": "true",
					},
				},
			},
			errContains: "multiple",
		},
		{
			name: "multi-name target",
			exports: []ExportEntry{{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": registry + "/buildkit/pushregval3a:latest," +
						registry + "/buildkit/pushregval3b:latest",
					"push":                 "true",
					"prefer-push-registry": "true",
				},
			}},
			errContains: "single",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Solve(sb.Context(), def, SolveOpt{Exports: tc.exports}, nil)
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tc.errContains))
		})
	}
}

// testParallelExtractWhiteouts is the regression test for parallel-extract's
// silent-loss-of-whiteouts bug. The scenario it guards against:
//
//   - A pulled image's layer chain includes a layer whose tar contains
//     `.wh.*` whiteout entries (any `RUN rm -rf …`, `apt-get clean`,
//     `pip cache purge`, etc. in a final-stage Dockerfile produces these).
//   - Parallel-extract handles each layer's tar standalone. If the applier
//     doesn't materialize whiteouts as overlay character devices, the
//     deletions silently no-op and the lower-layer files leak through into
//     the resulting overlay snapshot.
//
// The test builds a 2-layer image where the second layer deletes a file
// from the first, pushes it to the test registry, prunes the local cache
// to force a real pull on the next solve, then both:
//
//  1. Executes a child build that FROMs the pushed image and runs a
//     shell test asserting the deleted file is absent (the cmd executes
//     against the extracted snapshot, so a leak fails the build).
//  2. Local-exports the pushed image and asserts the file is missing
//     from the merged-overlay view.
//
// The test passes regardless of whether BUILDKIT_PARALLEL_EXTRACT is set
// — sequential extract has always handled this correctly. With
// parallel-extract enabled, the test's value is catching a regression in
// the overlay-typed-mount whiteout fix in cache/refs.go.
func testParallelExtractWhiteouts(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	// Step 1: build a 2-layer image. Layer 1 creates /data/keep and
	// /data/delete; Layer 2 deletes /data/delete (producing a `.wh.delete`
	// whiteout in layer 2's tar).
	src := llb.Image("busybox:latest").
		Run(llb.Shlex(`sh -c "mkdir -p /data && echo keep > /data/keep && echo delete > /data/delete"`)).Root().
		Run(llb.Shlex(`sh -c "rm -f /data/delete"`)).Root()
	def, err := src.Marshal(sb.Context())
	require.NoError(t, err)

	target := registry + "/buildkit/parallelextract-whiteout:latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Step 2: prune so the next solve has to actually pull (and re-extract)
	// — without this, buildkit reuses the locally-built snapshot and the
	// extract path isn't exercised at all.
	ensurePruneAll(t, c, sb)

	// Step 3a: child build that FROMs the pushed image. The pull triggers
	// parallel-extract on the layer chain. The Run command then asserts
	// the deletion took effect; if the whiteout was lost during extract,
	// /data/delete would be present and the build would fail with exit 1.
	child := llb.Image(target).
		Run(llb.Shlex(`sh -c "test -f /data/keep && ! test -e /data/delete"`)).Root()
	childDef, err := child.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), childDef, SolveOpt{}, nil)
	require.NoError(t, err, "deleted file from layer 2 must not leak through to child build")

	// Step 3b: also verify by local-exporting the image directly. The
	// local exporter mounts the image's layer chain as overlay and walks
	// the merged view; if whiteouts aren't overlay character devices,
	// the kernel won't mask /data/delete and it will appear here.
	ensurePruneAll(t, c, sb)
	pullDef, err := llb.Image(target).Marshal(sb.Context())
	require.NoError(t, err)
	destDir := t.TempDir()
	_, err = c.Solve(sb.Context(), pullDef, SolveOpt{
		Exports: []ExportEntry{
			{Type: ExporterLocal, OutputDir: destDir},
		},
	}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(destDir, "data/keep"))
	require.NoFileExists(t, filepath.Join(destDir, "data/delete"))
}

// --- helpers -------------------------------------------------------------

func readLayerDigests(t *testing.T, sb integration.Sandbox, ref string) []string {
	t.Helper()
	desc, provider, err := contentutil.ProviderFromRef(ref)
	require.NoError(t, err)
	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)
	require.NotEmpty(t, imgs.Images)
	var out []string
	for _, l := range imgs.Images[0].Manifest.Layers {
		out = append(out, l.Digest.String())
	}
	return out
}

func intersectStrings(a, b []string) []string {
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range b {
		if _, ok := set[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

func registryTagName(registry, base string, i int) string {
	return registry + "/buildkit/" + base + "-" + itoa(i) + ":latest"
}

// itoa is a tiny positive-int stringifier to keep fork_test.go free of the
// strconv import churn when we only need one or two digits.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
