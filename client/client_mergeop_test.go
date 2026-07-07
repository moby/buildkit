package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/continuity/fs/fstest"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testMergeOp(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureMergeDiff)
	requiresLinux(t)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	ctx := sb.Context()
	registry, err := sb.NewRegistry()
	if !errors.Is(err, integration.ErrRequirements) {
		require.NoError(t, err)
	}

	var imageTarget string
	if registry != "" {
		imageTarget = registry + "/buildkit/testmergeop:latest"
	}

	stateA := llb.Scratch().
		File(llb.Mkfile("/foo", 0755, []byte("A"))).
		File(llb.Mkfile("/a", 0755, []byte("A"))).
		File(llb.Mkdir("/bar", 0700)).
		File(llb.Mkfile("/bar/A", 0755, []byte("A")))
	stateB := stateA.
		File(llb.Rm("/foo")).
		File(llb.Mkfile("/b", 0755, []byte("B"))).
		File(llb.Mkfile("/bar/B", 0754, []byte("B")))
	stateC := llb.Scratch().
		File(llb.Mkfile("/foo", 0755, []byte("C"))).
		File(llb.Mkfile("/c", 0755, []byte("C"))).
		File(llb.Mkdir("/bar", 0755)).
		File(llb.Mkfile("/bar/A", 0400, []byte("C")))

	mergeA := llb.Merge([]llb.State{stateA, stateC})
	requireContents(ctx, t, c, sb, mergeA, nil, nil, imageTarget,
		fstest.CreateFile("foo", []byte("C"), 0755),
		fstest.CreateFile("c", []byte("C"), 0755),
		fstest.CreateDir("bar", 0755),
		fstest.CreateFile("bar/A", []byte("C"), 0400),
		fstest.CreateFile("a", []byte("A"), 0755),
	)

	mergeB := llb.Merge([]llb.State{stateC, stateB})
	requireContents(ctx, t, c, sb, mergeB, nil, nil, imageTarget,
		fstest.CreateFile("a", []byte("A"), 0755),
		fstest.CreateFile("b", []byte("B"), 0755),
		fstest.CreateFile("c", []byte("C"), 0755),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("bar/A", []byte("A"), 0755),
		fstest.CreateFile("bar/B", []byte("B"), 0754),
	)

	stateD := llb.Scratch().File(llb.Mkdir("/qaz", 0755))
	mergeC := llb.Merge([]llb.State{mergeA, mergeB, stateD})
	requireContents(ctx, t, c, sb, mergeC, nil, nil, imageTarget,
		fstest.CreateFile("a", []byte("A"), 0755),
		fstest.CreateFile("b", []byte("B"), 0755),
		fstest.CreateFile("c", []byte("C"), 0755),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("bar/A", []byte("A"), 0755),
		fstest.CreateFile("bar/B", []byte("B"), 0754),
		fstest.CreateDir("qaz", 0755),
	)

	runA := runShell(llb.Merge([]llb.State{llb.Image("alpine"), mergeC}),
		// turn /a file into a dir, mv b and c into it
		"rm /a",
		"mkdir /a",
		"mv /b /c /a/",
		// remove+recreate /bar to make it opaque on overlay snapshotters
		"rm -rf /bar",
		"mkdir -m 0755 /bar",
		"echo -n D > /bar/D",
		// turn /qaz dir into a file
		"rm -rf /qaz",
		"touch /qaz",
	)
	stateE := llb.Scratch().
		File(llb.Mkfile("/foo", 0755, []byte("E"))).
		File(llb.Mkdir("/bar", 0755)).
		File(llb.Mkfile("/bar/A", 0755, []byte("A"))).
		File(llb.Mkfile("/bar/E", 0755, nil))
	mergeD := llb.Merge([]llb.State{stateE, runA})
	requireEqualContents(ctx, t, c, mergeD, llb.Image("alpine").
		File(llb.Mkdir("a", 0755)).
		File(llb.Mkfile("a/b", 0755, []byte("B"))).
		File(llb.Mkfile("a/c", 0755, []byte("C"))).
		File(llb.Mkdir("bar", 0755)).
		File(llb.Mkfile("bar/D", 0644, []byte("D"))).
		File(llb.Mkfile("bar/E", 0755, nil)).
		File(llb.Mkfile("qaz", 0644, nil)),
	)
	// /foo from stateE is not here because it is deleted in stateB, which is part of a submerge of mergeD
}

func testMergeOpCache(t *testing.T, sb integration.Sandbox, mode string) {
	t.Helper()
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush, workers.FeatureMergeDiff)
	requiresLinux(t)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// push the busybox image to the mutable registry
	sourceImage := "busybox:latest"
	def, err := llb.Image(sourceImage).Marshal(sb.Context())
	require.NoError(t, err)

	busyboxTargetNoTag := registry + "/buildkit/testlazyimage:"
	busyboxTarget := busyboxTargetNoTag + "latest"
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   busyboxTarget,
					"push":                                   "true",
					"store":                                  "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	imageService := client.ImageService()
	contentStore := client.ContentStore()

	busyboxImg, err := imageService.Get(ctx, busyboxTarget)
	require.NoError(t, err)

	busyboxManifest, err := images.Manifest(ctx, contentStore, busyboxImg.Target, nil)
	require.NoError(t, err)

	for _, layer := range busyboxManifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	// clear all local state out
	err = imageService.Delete(ctx, busyboxImg.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range busyboxManifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// make a new merge that includes the lazy busybox as a base and exports inline cache
	input1 := llb.Scratch().
		File(llb.Mkdir("/dir", 0777)).
		File(llb.Mkfile("/dir/1", 0777, nil))
	input1Copy := llb.Scratch().File(llb.Copy(input1, "/dir/1", "/foo/1", &llb.CopyInfo{CreateDestPath: true}))

	// put random contents in the file to ensure it's not re-run later
	input2 := runShell(llb.Image("alpine:latest"),
		"mkdir /dir",
		"cat /dev/urandom | head -c 100 | sha256sum > /dir/2")
	input2Copy := llb.Scratch().File(llb.Copy(input2, "/dir/2", "/bar/2", &llb.CopyInfo{CreateDestPath: true}))

	merge := llb.Merge([]llb.State{llb.Image(busyboxTarget), input1Copy, input2Copy})

	def, err = merge.Marshal(sb.Context())
	require.NoError(t, err)

	target := registry + "/buildkit/testmerge:latest"
	cacheTarget := registry + "/buildkit/testmergecache:latest"

	var cacheExports []CacheOptionsEntry
	var cacheImports []CacheOptionsEntry
	switch mode {
	case "inline":
		cacheExports = []CacheOptionsEntry{{
			Type: "inline",
		}}
		cacheImports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": target,
			},
		}}
	case "min":
		cacheExports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": cacheTarget,
			},
		}}
		cacheImports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": cacheTarget,
			},
		}}
	case "max":
		cacheExports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref":  cacheTarget,
				"mode": "max",
			},
		}}
		cacheImports = []CacheOptionsEntry{{
			Type: "registry",
			Attrs: map[string]string{
				"ref": cacheTarget,
			},
		}}
	default:
		require.Fail(t, fmt.Sprintf("unknown cache mode: %s", mode))
	}

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target,
					"push":                                   "true",
					"store":                                  "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	// verify that the busybox image stayed lazy
	for _, layer := range busyboxManifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// get the random value at /bar/2
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

	bar2Contents, err := os.ReadFile(filepath.Join(destDir, "bar", "2"))
	require.NoError(t, err)

	// clear all local state out
	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
	}

	// re-run the same build with cache imports and verify everything stays lazy
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name":                                   target,
				"push":                                   "true",
				"store":                                  "true",
				"unsafe-internal-store-allow-incomplete": "true",
			},
		}},
		CacheImports: cacheImports,
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	// verify everything from before stayed lazy
	img, err = imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for i, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v for index %d (%s)", err, i, layer.Digest)
	}

	// re-run the build with a change only to input1 using the remote cache
	input1 = llb.Scratch().
		File(llb.Mkdir("/dir", 0777)).
		File(llb.Mkfile("/dir/1", 0444, nil))
	input1Copy = llb.Scratch().File(llb.Copy(input1, "/dir/1", "/foo/1", &llb.CopyInfo{CreateDestPath: true}))

	merge = llb.Merge([]llb.State{llb.Image(busyboxTarget), input1Copy, input2Copy})

	def, err = merge.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name":                                   target,
				"push":                                   "true",
				"store":                                  "true",
				"unsafe-internal-store-allow-incomplete": "true",
			},
		}},
		CacheExports: cacheExports,
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	// verify everything from before stayed lazy except the middle layer for input1Copy
	img, err = imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for i, layer := range manifest.Layers {
		switch i {
		case 0, 2:
			// bottom and top layer should stay lazy as they didn't change
			_, err = contentStore.Info(ctx, layer.Digest)
			require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v for index %d", err, i)
		case 1:
			// middle layer had to be rebuilt, should exist locally
			_, err = contentStore.Info(ctx, layer.Digest)
			require.NoError(t, err)
		default:
			require.Fail(t, fmt.Sprintf("unexpected layer index %d", i))
		}
	}

	// check the random value at /bar/2 didn't change
	destDir = t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	newBar2Contents, err := os.ReadFile(filepath.Join(destDir, "bar", "2"))
	require.NoError(t, err)

	require.Equalf(t, bar2Contents, newBar2Contents, "bar/2 contents changed")

	// Now test the case with a layer on top of a merge.
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
	}

	mergePlusLayer := merge.File(llb.Mkfile("/3", 0444, nil))

	def, err = mergePlusLayer.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name":                                   target,
				"push":                                   "true",
				"store":                                  "true",
				"unsafe-internal-store-allow-incomplete": "true",
			},
		}},
		CacheExports: cacheExports,
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	// check the random value at /bar/2 didn't change
	destDir = t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: cacheImports,
	}, nil)
	require.NoError(t, err)

	newBar2Contents, err = os.ReadFile(filepath.Join(destDir, "bar", "2"))
	require.NoError(t, err)

	require.Equalf(t, bar2Contents, newBar2Contents, "bar/2 contents changed")

	// clear local state, repeat the build, verify everything stays lazy
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	for _, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
	}

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{{
			Type: ExporterImage,
			Attrs: map[string]string{
				"name":                                   target,
				"push":                                   "true",
				"store":                                  "true",
				"unsafe-internal-store-allow-incomplete": "true",
			},
		}},
		CacheImports: cacheImports,
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	img, err = imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err = images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	for i, layer := range manifest.Layers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v for index %d", err, i)
	}
}

func testMergeOpCacheInline(t *testing.T, sb integration.Sandbox) {
	testMergeOpCache(t, sb, "inline")
}

func testMergeOpCacheMax(t *testing.T, sb integration.Sandbox) {
	testMergeOpCache(t, sb, "max")
}

func testMergeOpCacheMin(t *testing.T, sb integration.Sandbox) {
	testMergeOpCache(t, sb, "min")
}

func chainRunShells(base llb.State, cmdss ...[]string) llb.State {
	for _, cmds := range cmdss {
		base = runShell(base, cmds...)
	}
	return base
}

func requireContents(ctx context.Context, t *testing.T, c *Client, sb integration.Sandbox, state llb.State, cacheImports, cacheExports []CacheOptionsEntry, imageTarget string, files ...fstest.Applier) {
	t.Helper()

	def, err := state.Marshal(ctx)
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: cacheImports,
		CacheExports: cacheExports,
	}, nil)
	require.NoError(t, err)

	require.NoError(t, fstest.CheckDirectoryEqualWithApplier(destDir, fstest.Apply(files...)))

	if imageTarget != "" {
		_, err = c.Solve(ctx, def, SolveOpt{
			Exports: []ExportEntry{{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": imageTarget,
					"push": "true",
				},
			}},
			CacheImports: cacheImports,
			CacheExports: cacheExports,
		}, nil)
		require.NoError(t, err)
		resetState(t, c, sb)
		requireContents(ctx, t, c, sb, llb.Image(imageTarget, llb.ResolveModePreferLocal), cacheImports, nil, "", files...)
	}
}

func requireEqualContents(ctx context.Context, t *testing.T, c *Client, stateA, stateB llb.State) {
	t.Helper()

	defA, err := stateA.Marshal(ctx)
	require.NoError(t, err)

	destDirA := t.TempDir()

	_, err = c.Solve(ctx, defA, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDirA,
			},
		},
	}, nil)
	require.NoError(t, err)

	defB, err := stateB.Marshal(ctx)
	require.NoError(t, err)

	destDirB := t.TempDir()

	_, err = c.Solve(ctx, defB, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDirB,
			},
		},
	}, nil)
	require.NoError(t, err)

	require.NoError(t, fstest.CheckDirectoryEqual(destDirA, destDirB))
}

func runShell(base llb.State, cmds ...string) llb.State {
	return runShellExecState(base, cmds...).Root()
}

func runShellExecState(base llb.State, cmds ...string) llb.ExecState {
	return base.Run(llb.Args([]string{"sh", "-c", strings.Join(cmds, " && ")}))
}
