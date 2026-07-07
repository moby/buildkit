package client

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	cerrdefs "github.com/containerd/errdefs"
	cacheimporttypes "github.com/moby/buildkit/cache/remotecache/v1/types"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/helpers"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testBasicAzblobCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendAzblob,
	)

	opts := helpers.AzuriteOpts{
		AccountName: "azblobcacheaccount",
		AccountKey:  base64.StdEncoding.EncodeToString([]byte("azblobcacheaccountkey")),
	}

	azAddr, cleanup, err := helpers.NewAzuriteServer(t, sb, opts)
	require.NoError(t, err)
	defer cleanup()

	im := CacheOptionsEntry{
		Type: "azblob",
		Attrs: map[string]string{
			"account_url":       azAddr,
			"account_name":      opts.AccountName,
			"secret_access_key": opts.AccountKey,
			"container":         "cachecontainer",
		},
	}
	ex := CacheOptionsEntry{
		Type: "azblob",
		Attrs: map[string]string{
			"account_url":       azAddr,
			"account_name":      opts.AccountName,
			"secret_access_key": opts.AccountKey,
			"container":         "cachecontainer",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testBasicCacheImportExport(t *testing.T, sb integration.Sandbox, cacheOptionsEntryImport, cacheOptionsEntryExport []CacheOptionsEntry) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))

	run := func(cmd string) {
		baseState := integration.UnixOrWindows(busybox, st)
		st = baseState.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(integration.UnixOrWindows(
		`sh -c "echo -n foobar > const"`,
		`cmd /C echo foobar > const`,
	))
	run(integration.UnixOrWindows(
		`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`,
		`cmd /C echo %RANDOM% > unique`,
	))

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
		CacheExports: cacheOptionsEntryExport,
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	newLine := integration.UnixOrWindows("", " \r\n")
	require.Equal(t, "foobar"+newLine, string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	destDir = t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: cacheOptionsEntryImport,
	}, nil)
	require.NoError(t, err)

	dt2, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, "foobar"+newLine, string(dt2))

	dt2, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))
}

func testBasicInlineCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureDirectPush,
		workers.FeatureCacheExport,
		workers.FeatureCacheBackendInline,
	)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(integration.UnixOrWindows(
		`sh -c "echo -n foobar > const"`,
		`cmd /C echo foobar > const`,
	))
	run(integration.UnixOrWindows(
		`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`,
		`cmd /C echo %RANDOM% > unique`,
	))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	target := registry + "/buildkit/testexportinline:latest"

	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	unique, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst, "/unique")
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheImport, workers.FeatureCacheBackendRegistry)

	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		// specifying inline cache exporter is needed for reproducing containerimage.digest
		// (not needed for reproducing rootfs/unique)
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": target,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	require.Equal(t, dgst, dgst2)

	ensurePruneAll(t, c, sb)

	// Export the cache again with compression
	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		// specifying inline cache exporter is needed for reproducing containerimage.digest
		// (not needed for reproducing rootfs/unique)
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              target,
					"push":              "true",
					"compression":       "uncompressed", // inline cache should work with compression
					"force-compression": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": target,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2uncompress, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	// dgst2uncompress != dgst, because the compression type is different
	unique2uncompress, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst2uncompress, "/unique")
	require.NoError(t, err)
	require.Equal(t, unique, unique2uncompress)

	ensurePruneAll(t, c, sb)

	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": target,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst3, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	// dgst3 != dgst, because inline cache is not exported for dgst3
	unique3, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst3, "/unique")
	require.NoError(t, err)
	require.Equal(t, unique, unique3)
}

func testBasicLocalCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendLocal,
	)
	dir := t.TempDir()
	im := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"src": dir,
		},
	}
	ex := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"dest": dir,
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testBasicRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
	)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	o := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{o}, []CacheOptionsEntry{o})
}

func testBasicS3CacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendS3,
	)

	opts := helpers.MinioOpts{
		Region:          "us-east-1",
		AccessKeyID:     "minioadmin",
		SecretAccessKey: "minioadmin",
	}

	s3Addr, s3Bucket, cleanup, err := helpers.NewMinioServer(t, sb, opts)
	require.NoError(t, err)
	defer cleanup()

	im := CacheOptionsEntry{
		Type: "s3",
		Attrs: map[string]string{
			"region":            opts.Region,
			"access_key_id":     opts.AccessKeyID,
			"secret_access_key": opts.SecretAccessKey,
			"bucket":            s3Bucket,
			"endpoint_url":      s3Addr,
			"use_path_style":    "true",
		},
	}
	ex := CacheOptionsEntry{
		Type: "s3",
		Attrs: map[string]string{
			"region":            opts.Region,
			"access_key_id":     opts.AccessKeyID,
			"secret_access_key": opts.SecretAccessKey,
			"bucket":            s3Bucket,
			"endpoint_url":      s3Addr,
			"use_path_style":    "true",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testCacheExportCacheDeletedContent(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheBackendLocal)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	tmpdir := integration.Tmpdir(t)

	err = os.WriteFile(filepath.Join(tmpdir.Name, "foo"), []byte("foodata"), 0600)
	require.NoError(t, err)

	base := llb.Image("alpine:latest").Run(llb.Shlex(`sh -c "echo abc-def > /foo && echo abc > /detection-file"`)).Root()
	st := llb.Scratch().File(llb.Copy(base, "/foo", "/out"))

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		CacheExports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest": filepath.Join(tmpdir.Name, "cache"),
					"mode": "max",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(tmpdir.Name, "cache", "index.json"))
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(dt, &index)
	require.NoError(t, err)

	require.Len(t, index.Manifests, 1)

	dt, err = os.ReadFile(filepath.Join(tmpdir.Name, "cache/blobs/sha256", index.Manifests[0].Digest.Hex()))
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)

	require.Len(t, mfst.Layers, 3)
	require.Equal(t, "application/vnd.buildkit.cacheconfig.v0", mfst.Config.MediaType)

	dt, err = os.ReadFile(filepath.Join(tmpdir.Name, "cache/blobs/sha256", mfst.Config.Digest.Hex()))
	require.NoError(t, err)

	var cc cacheimporttypes.CacheConfig
	err = json.Unmarshal(dt, &cc)
	require.NoError(t, err)

	require.Equal(t, 3, len(cc.Layers))
	require.Equal(t, 5, len(cc.Records))

	var runLayer *int
	for i, l := range cc.Layers {
		if l.ParentIndex != -1 {
			if runLayer != nil {
				t.Fatal("multiple RUN layers")
			}
			runLayer = &i
		}
	}
	require.NotNil(t, runLayer)

	dt, err = os.ReadFile(filepath.Join(tmpdir.Name, "cache/blobs/sha256", cc.Layers[*runLayer].Blob.Hex()))
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, true)
	require.NoError(t, err)

	require.Equal(t, "abc\n", string(m["detection-file"].Data))

	// delete the blob for the run layer
	err = os.Remove(filepath.Join(tmpdir.Name, "cache/blobs/sha256", cc.Layers[*runLayer].Blob.Hex()))
	require.NoError(t, err)

	// prune all buildkit state
	ensurePruneAll(t, c, sb)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		CacheImports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"src": filepath.Join(tmpdir.Name, "cache"),
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest": filepath.Join(tmpdir.Name, "cache2"),
					"mode": "max",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(tmpdir.Name, "cache2", "index.json"))
	require.NoError(t, err)

	var index2 ocispecs.Index
	err = json.Unmarshal(dt, &index2)
	require.NoError(t, err)

	require.Len(t, index2.Manifests, 1)

	dt, err = os.ReadFile(filepath.Join(tmpdir.Name, "cache2/blobs/sha256", index2.Manifests[0].Digest.Hex()))
	require.NoError(t, err)

	var mfst2 ocispecs.Manifest
	err = json.Unmarshal(dt, &mfst2)
	require.NoError(t, err)

	require.Len(t, mfst2.Layers, 1)
	require.Equal(t, "application/vnd.buildkit.cacheconfig.v0", mfst2.Config.MediaType)

	dt, err = os.ReadFile(filepath.Join(tmpdir.Name, "cache2/blobs/sha256", mfst2.Config.Digest.Hex()))
	require.NoError(t, err)

	var cc2 cacheimporttypes.CacheConfig
	err = json.Unmarshal(dt, &cc2)
	require.NoError(t, err)

	require.Equal(t, 1, len(cc2.Layers))
	require.Equal(t, 5, len(cc2.Records))

	for i, r := range cc.Records {
		require.Equal(t, cc2.Records[i].Digest, r.Digest)
		require.Equal(t, cc2.Records[i].Inputs, r.Inputs)
	}
}

// moby/buildkit#1336
func testCacheExportCacheKeyLoop(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheBackendLocal)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	tmpdir := integration.Tmpdir(t)

	err = os.WriteFile(filepath.Join(tmpdir.Name, "foo"), []byte("foodata"), 0600)
	require.NoError(t, err)

	imgName := integration.UnixOrWindows("alpine:latest", "nanoserver:latest")
	scratch := integration.UnixOrWindows(
		llb.Scratch(),
		llb.Image(imgName),
	)
	cmdStr := integration.UnixOrWindows("true", "cmd /C exit 0")
	for _, mode := range []bool{false, true} {
		func(mode bool) {
			t.Run(fmt.Sprintf("mode=%v", mode), func(t *testing.T) {
				buildbase := llb.Image(imgName).File(llb.Copy(llb.Local("mylocal"), "foo", "foo"))
				if mode { // same cache keys with a separating node go to different code-path
					buildbase = buildbase.Run(llb.Shlex(cmdStr)).Root()
				}
				intermed := llb.Image(imgName).File(llb.Copy(buildbase, "foo", "foo"))
				final := scratch.File(llb.Copy(intermed, "foo", "foooooo"))

				def, err := final.Marshal(sb.Context())
				require.NoError(t, err)

				_, err = c.Solve(sb.Context(), def, SolveOpt{
					CacheExports: []CacheOptionsEntry{
						{
							Type: "local",
							Attrs: map[string]string{
								"dest": filepath.Join(tmpdir.Name, "cache"),
							},
						},
					},
					LocalMounts: map[string]fsutil.FS{
						"mylocal": tmpdir,
					},
				}, nil)
				require.NoError(t, err)
			})
		}(mode)
	}
}

func testCacheExportIgnoreError(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	cmd := integration.UnixOrWindows(
		`sh -e -c "echo -n ignore-error > data"`,
		"cmd /C echo ignore-error> data",
	)

	st := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))
	st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	tests := map[string]struct {
		Exports        []ExportEntry
		CacheExports   []CacheOptionsEntry
		expectedErrors []string
	}{
		"local-ignore-error": {
			Exports: []ExportEntry{
				{
					Type:      ExporterLocal,
					OutputDir: t.TempDir(),
				},
			},
			CacheExports: []CacheOptionsEntry{
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "éèç",
					},
				},
			},
			expectedErrors: []string{"failed to solve", "contains value with non-printable ASCII characters"},
		},
		"registry-ignore-error": {
			Exports: []ExportEntry{
				{
					Type: ExporterImage,
					Attrs: map[string]string{
						"name": "test-registry-ignore-error",
						"push": "false",
					},
				},
			},
			CacheExports: []CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "fake-url:5000/myrepo:buildcache",
					},
				},
			},
			expectedErrors: []string{"failed to solve", "dial tcp: lookup fake-url", "no such host"},
		},
		"s3-ignore-error": {
			Exports: []ExportEntry{
				{
					Type:      ExporterLocal,
					OutputDir: t.TempDir(),
				},
			},
			CacheExports: []CacheOptionsEntry{
				{
					Type: "s3",
					Attrs: map[string]string{
						"endpoint_url":      "http://fake-url:9000",
						"bucket":            "my-bucket",
						"region":            "us-east-1",
						"access_key_id":     "minioadmin",
						"secret_access_key": "minioadmin",
						"use_path_style":    "true",
					},
				},
			},
			expectedErrors: []string{"failed to solve", "dial tcp: lookup fake-url", "no such host"},
		},
	}
	ignoreErrorValues := []bool{true, false}
	for _, ignoreError := range ignoreErrorValues {
		ignoreErrStr := strconv.FormatBool(ignoreError)
		for n, test := range tests {
			require.Equal(t, 1, len(test.Exports))
			require.Equal(t, 1, len(test.CacheExports))
			require.NotEmpty(t, test.CacheExports[0].Attrs)
			test.CacheExports[0].Attrs["ignore-error"] = ignoreErrStr
			testName := fmt.Sprintf("%s-%s", n, ignoreErrStr)
			t.Run(testName, func(t *testing.T) {
				switch n {
				case "local-ignore-error":
					workers.CheckFeatureCompat(t, sb, workers.FeatureCacheBackendLocal)
				case "registry-ignore-error":
					workers.CheckFeatureCompat(t, sb, workers.FeatureCacheBackendRegistry)
				case "s3-ignore-error":
					workers.CheckFeatureCompat(t, sb, workers.FeatureCacheBackendS3)
				}
				_, err = c.Solve(sb.Context(), def, SolveOpt{
					Exports:      test.Exports,
					CacheExports: test.CacheExports,
				}, nil)
				if ignoreError {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					for _, errStr := range test.expectedErrors {
						require.Contains(t, err.Error(), errStr)
					}
				}
			})
		}
	}
}

func testImageManifestRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
	)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	im := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	ex := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref":            target,
			"oci-mediatypes": "true",
			"mode":           "max",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testLocalCacheExportReset(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheBackendLocal)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	tmpdir := integration.Tmpdir(t)
	cacheDir := filepath.Join(tmpdir.Name, "cache")

	// Build 1: export cache for a simple build
	st1 := llb.Image("alpine:latest").Run(llb.Shlex(`sh -c "echo build1 > /out"`)).Root()
	def1, err := llb.Scratch().File(llb.Copy(st1, "/out", "/out")).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def1, SolveOpt{
		CacheExports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest": cacheDir,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Verify blobs were written
	blobDir := filepath.Join(cacheDir, "blobs", "sha256")
	entries1, err := os.ReadDir(blobDir)
	require.NoError(t, err)
	require.Greater(t, len(entries1), 0)

	// Build 2: different build, export with reset=true
	st2 := llb.Image("alpine:latest").Run(llb.Shlex(`sh -c "echo build2-different > /out2"`)).Root()
	def2, err := llb.Scratch().File(llb.Copy(st2, "/out2", "/out2")).Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def2, SolveOpt{
		CacheExports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest":  cacheDir,
					"reset": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Read the manifest to determine which blobs should be referenced
	dt, err := os.ReadFile(filepath.Join(cacheDir, "index.json"))
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(dt, &index)
	require.NoError(t, err)
	require.Len(t, index.Manifests, 1)

	dt, err = os.ReadFile(filepath.Join(blobDir, index.Manifests[0].Digest.Hex()))
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(dt, &mfst)
	require.NoError(t, err)

	// Build the set of expected blobs: manifest + config + all layers
	expectedBlobs := map[string]struct{}{
		index.Manifests[0].Digest.Hex(): {},
		mfst.Config.Digest.Hex():        {},
	}
	for _, l := range mfst.Layers {
		expectedBlobs[l.Digest.Hex()] = struct{}{}
	}

	// Verify only referenced blobs remain
	entries2, err := os.ReadDir(blobDir)
	require.NoError(t, err)

	actualBlobs := map[string]struct{}{}
	for _, e := range entries2 {
		actualBlobs[e.Name()] = struct{}{}
	}
	require.Equal(t, expectedBlobs, actualBlobs, "after reset, only blobs referenced by the current manifest should remain")
}

func testMultipleCacheExports(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureMultiCacheExport)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	busybox := llb.Image(imgName)
	st := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	constCmd := integration.UnixOrWindows(
		`sh -c "echo -n foobar > const"`,
		`cmd /C echo foobar > const`,
	)
	uniqueCmd := integration.UnixOrWindows(
		`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`,
		`cmd /C echo %RANDOM% > unique`,
	)

	run(constCmd)
	run(uniqueCmd)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	target := path.Join(registry, "image:test")
	target2 := path.Join(registry, "image-copy:test")
	cacheRef := path.Join(registry, "cache:test")
	cacheOutDir, cacheOutDir2 := t.TempDir(), t.TempDir()

	res, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest": cacheOutDir,
				},
			},
			{
				Type: "local",
				Attrs: map[string]string{
					"dest": cacheOutDir2,
				},
			},
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": cacheRef,
				},
			},
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	ensureFile(t, filepath.Join(cacheOutDir, ocispecs.ImageIndexFile))
	ensureFile(t, filepath.Join(cacheOutDir2, ocispecs.ImageIndexFile))

	dgst := res.ExporterResponse[exptypes.ExporterImageDigestKey]

	uniqueFile, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst, "/unique")
	require.NoError(t, err)

	res, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name": target2,
					"push": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2 := res.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, dgst, dgst2)

	destDir := t.TempDir()
	ensurePruneAll(t, c, sb)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": cacheRef,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	ensureFileContents(t, filepath.Join(destDir, "const"), integration.UnixOrWindows("foobar", "foobar \r\n"))
	ensureFileContents(t, filepath.Join(destDir, "unique"), string(uniqueFile))
}

func testMultipleRecordsWithSameLayersCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
	)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	cacheOpts := []CacheOptionsEntry{{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}}

	requiresLinux(t)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	base := llb.Image("busybox:latest")
	// layerA and layerB create identical layers with different LLB
	layerA := base.Run(llb.Args([]string{
		"sh", "-c",
		`echo $(( 1 + 2 )) > /result && touch -d "1970-01-01 00:00:00" /result`,
	})).Root()
	layerB := base.Run(llb.Args([]string{
		"sh", "-c",
		`echo $(( 2 + 1 )) > /result && touch -d "1970-01-01 00:00:00" /result`,
	})).Root()

	combined := llb.Merge([]llb.State{layerA, layerB})
	combinedDef, err := combined.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), combinedDef, SolveOpt{
		CacheExports: cacheOpts,
	}, nil)
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	singleDef, err := layerA.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), singleDef, SolveOpt{
		CacheImports: cacheOpts,
	}, nil)
	require.NoError(t, err)

	// Ensure that even though layerA and layerB were both loaded as possible results
	// and only was used, all the cache refs are released
	// More context: https://github.com/moby/buildkit/pull/3815
	ensurePruneAll(t, c, sb)
}

func testMultipleRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
	)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	o := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	o2 := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target + "notexist",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{o, o2}, []CacheOptionsEntry{o})
}

func testRegistryEmptyCacheExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheBackendRegistry,
	)

	for _, ociMediaTypes := range []bool{true, false} {
		for _, imageManifest := range []bool{true, false} {
			if imageManifest && !ociMediaTypes {
				// invalid configuration for remote cache
				continue
			}

			t.Run(t.Name()+fmt.Sprintf("/ociMediaTypes=%t/imageManifest=%t", ociMediaTypes, imageManifest), func(t *testing.T) {
				c, err := New(sb.Context(), sb.Address())
				require.NoError(t, err)
				defer c.Close()

				st := llb.Scratch()
				def, err := st.Marshal(sb.Context())
				require.NoError(t, err)

				registry, err := sb.NewRegistry()
				if errors.Is(err, integration.ErrRequirements) {
					t.Skip(err.Error())
				}
				require.NoError(t, err)

				cacheTarget := registry + "/buildkit/testregistryemptycache:latest"

				cacheOptionsEntry := CacheOptionsEntry{
					Type: "registry",
					Attrs: map[string]string{
						"ref":            cacheTarget,
						"image-manifest": strconv.FormatBool(imageManifest),
						"oci-mediatypes": strconv.FormatBool(ociMediaTypes),
					},
				}

				_, err = c.Solve(sb.Context(), def, SolveOpt{
					CacheExports: []CacheOptionsEntry{cacheOptionsEntry},
				}, nil)
				require.NoError(t, err)

				ctx := namespaces.WithNamespace(sb.Context(), "buildkit")
				cdAddress := sb.ContainerdAddress()
				var client *ctd.Client
				if cdAddress != "" {
					client, err = newContainerd(cdAddress)
					require.NoError(t, err)
					defer client.Close()

					_, err := client.Fetch(ctx, cacheTarget)
					require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v", err)
				}
			})
		}
	}
}

// testSnapshotWithMultipleBlobs verifies that BuildKit handles a single snapshot with multiple
// compressed blob representations. It pushes the same layer with compression levels 0 and 9,
// producing different blob digests, then pulls and exports both images to ensure no conflicts.
// moby/buildkit#3809
func testSnapshotWithMultipleBlobs(t *testing.T, sb integration.Sandbox) {
	// TODO: Re-enable on Windows once the HCS layer activation race is fixed upstream.
	// Passes locally but fails on GitHub Actions with: mkdir ...\ProgramData\Microsoft:
	// Cannot create a file when that file already exists. The bug is in the Windows
	// snapshotter/differ layer activation path, not in this test.
	integration.SkipOnPlatform(t, "windows", "HCS layer activation race: concurrent mkdir fails on CI (passes locally)")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// build two images with same layer but different compressed blobs

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	now := time.Now()

	imgName := integration.UnixOrWindows("alpine", "nanoserver")
	// On Windows:
	// - llb.Scratch() doesn't work (Windows snapshotter requires a base OS layer)
	// - llb.Copy across images doesn't work (dual mount: "number of mounts should always be 1")
	// So we use llb.Image(nanoserver).File(llb.Mkfile(...)) which only needs one mount.
	// The nanoserver base layers appear before our layer, so the final assertion
	// compares a layer index that skips the base layers.
	st := integration.UnixOrWindows(
		llb.Scratch().File(llb.Copy(llb.Image(imgName), "/", "/"+imgName+"/", llb.WithCreatedTime(now))),
		llb.Image(imgName).File(llb.Mkfile("/testdata", 0600, []byte("snapshot-test-content"), llb.WithCreatedTime(now))),
	)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	name1 := registry + "/multiblobtest1/image:latest"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: "image",
				Attrs: map[string]string{
					"name":              name1,
					"push":              "true",
					"compression-level": "0",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	st = st.File(llb.Mkfile("test", 0600, []byte("test"))) // extra layer so we don't get a cache match based on image config rootfs only

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	name2 := registry + "/multiblobtest2/image:latest"

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: "image",
				Attrs: map[string]string{
					"name":              name2,
					"push":              "true",
					"compression-level": "9",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	// first build with first image
	destDir := t.TempDir()

	out1 := filepath.Join(destDir, "out1.tar")
	outW1, err := os.Create(out1)
	require.NoError(t, err)

	st = llb.Image(name1).File(llb.Mkfile("test", 0600, []byte("test1")))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI, // make sure to export so blobs need to be loaded
				Output: fixedWriteCloser(outW1),
			},
		},
	}, nil)
	require.NoError(t, err)

	// make sure second image does not cause any errors
	out2 := filepath.Join(destDir, "out2.tar")
	outW2, err := os.Create(out2)
	require.NoError(t, err)

	st = llb.Image(name2).File(llb.Mkfile("test", 0600, []byte("test2")))

	def, err = st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		FrontendAttrs: integration.UnixOrWindows(
			map[string]string{"attest:sbom": ""},
			map[string]string{},
		),
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI, // make sure to export so blobs need to be loaded
				Output: fixedWriteCloser(outW2),
			},
		},
	}, nil)
	require.NoError(t, err)

	// extra validation that we did get different layer blobs
	dt, err := os.ReadFile(out1)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var index ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	dt, err = os.ReadFile(out2)
	require.NoError(t, err)

	m, err = testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index)
	require.NoError(t, err)

	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &index)
	require.NoError(t, err)

	var mfst2 ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst2)
	require.NoError(t, err)

	// On Linux, Layers[0] is the layer we created (copy from alpine).
	// On Windows, the nanoserver base layer(s) come first, so our layer is at a later index.
	// We find the first layer that differs between the two manifests — that's the layer
	// pushed with different compression levels.
	layerIdx := integration.UnixOrWindows(0, len(mfst.Layers)-2)
	require.NotEqual(t, mfst.Layers[layerIdx].Digest, mfst2.Layers[layerIdx].Digest)
}

func testUncompressedLocalCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendLocal,
	)
	dir := t.TempDir()
	im := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"src": dir,
		},
	}
	ex := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"dest":              dir,
			"compression":       "uncompressed",
			"force-compression": "true",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testUncompressedRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
	)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	im := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	ex := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref":               target,
			"compression":       "uncompressed",
			"force-compression": "true",
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

// TODO: Re-enable the skipped test on Windows (see issue #6296)
func testZstdLocalCacheExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "This test passed locally on windows, but failed on github action")
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheExport, workers.FeatureCacheBackendLocal)
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	cmdStr := integration.UnixOrWindows(
		`sh -e -c "echo -n zstd > data"`,
		`cmd /C "echo zstd > C:\data"`,
	)

	st := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))
	st = llb.Image(imgName).Run(llb.Shlex(cmdStr), llb.Dir("/wd")).AddMount("/wd", st)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()
	destOutDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destOutDir,
			},
		},
		// compression option should work even with inline cache exports
		CacheExports: []CacheOptionsEntry{
			{
				Type: "local",
				Attrs: map[string]string{
					"dest":           destDir,
					"image-manifest": "false",
					"compression":    "zstd",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	var index ocispecs.Index
	dt, err := os.ReadFile(filepath.Join(destDir, ocispecs.ImageIndexFile))
	require.NoError(t, err)
	err = json.Unmarshal(dt, &index)
	require.NoError(t, err)

	var layerIndex ocispecs.Index
	dt, err = os.ReadFile(filepath.Join(destDir, ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()))
	require.NoError(t, err)
	err = json.Unmarshal(dt, &layerIndex)
	require.NoError(t, err)

	lastLayer := layerIndex.Manifests[len(layerIndex.Manifests)-2]
	require.Equal(t, ocispecs.MediaTypeImageLayerZstd, lastLayer.MediaType)

	zstdLayerDigest := lastLayer.Digest.Hex()
	dt, err = os.ReadFile(filepath.Join(destDir, ocispecs.ImageBlobsDir+"/sha256/"+zstdLayerDigest))
	require.NoError(t, err)
	require.Equal(t, []byte{0x28, 0xb5, 0x2f, 0xfd}, dt[:4])
}

func testZstdLocalCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")

	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendLocal,
	)
	dir := t.TempDir()
	im := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"src": dir,
		},
	}
	ex := CacheOptionsEntry{
		Type: "local",
		Attrs: map[string]string{
			"dest":              dir,
			"compression":       "zstd",
			"force-compression": "true",
			"oci-mediatypes":    "true", // containerd applier supports only zstd with oci-mediatype.
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

// TODO: Re-enable the skipped test on Windows (see issue #6296)
func testZstdRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "This test passed locally on windows, but failed on github action")

	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendRegistry,
	)
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)
	target := registry + "/buildkit/testexport:latest"
	im := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref": target,
		},
	}
	ex := CacheOptionsEntry{
		Type: "registry",
		Attrs: map[string]string{
			"ref":               target,
			"compression":       "zstd",
			"force-compression": "true",
			"image-manifest":    "false",
			"oci-mediatypes":    "true", // containerd applier supports only zstd with oci-mediatype.
		},
	}
	testBasicCacheImportExport(t, sb, []CacheOptionsEntry{im}, []CacheOptionsEntry{ex})
}

func testStargzLazyInlineCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendInline,
		workers.FeatureCacheBackendRegistry,
	)
	requiresLinux(t)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" || sb.Snapshotter() != "stargz" {
		t.Skip("test requires containerd worker with stargz snapshotter")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	var (
		imageService = client.ImageService()
		contentStore = client.ContentStore()
		ctx          = namespaces.WithNamespace(sb.Context(), "buildkit")
	)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	// Prepare stargz inline cache
	orgImage := "docker.io/library/alpine:latest"
	sgzImage := registry + "/stargz/alpine:" + identity.NewID()
	baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/foo"}))
	def, err := baseDef.Marshal(sb.Context())
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":              sgzImage,
					"push":              "true",
					"compression":       "estargz",
					"oci-mediatypes":    "true",
					"force-compression": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
	}, nil)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, sgzImage, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be lazy even for executing something on them
	def, err = baseDef.
		Run(llb.Args([]string{"/bin/touch", "/bar"})).
		Marshal(sb.Context())
	require.NoError(t, err)
	target := registry + "/buildkit/testlazyimage:" + identity.NewID()
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target,
					"push":                                   "true",
					"oci-mediatypes":                         "true",
					"compression":                            "estargz",
					"store":                                  "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "inline",
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzImage,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	// Check if image layers are lazy.
	// The topmost(last) layer created by `Run` isn't lazy so we skip the check for the layer.
	var sgzLayers []ocispecs.Descriptor
	for i, layer := range manifest.Layers[:len(manifest.Layers)-1] {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v on layer %+v (%d)", err, layer, i)
		sgzLayers = append(sgzLayers, layer)
	}
	require.NotEqual(t, 0, len(sgzLayers), "no layer can be used for checking lazypull")

	// The topmost(last) layer created by `Run` shouldn't be lazy
	_, err = contentStore.Info(ctx, manifest.Layers[len(manifest.Layers)-1].Digest)
	require.NoError(t, err)

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be exportable
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
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzImage,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Check if image layers are un-lazied
	for _, layer := range sgzLayers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	ensurePruneAll(t, c, sb)
}

func testStargzLazyRegistryCacheImportExport(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheBackendRegistry,
		workers.FeatureOCIExporter,
	)
	requiresLinux(t)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" || sb.Snapshotter() != "stargz" {
		t.Skip("test requires containerd worker with stargz snapshotter")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()
	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	var (
		imageService = client.ImageService()
		contentStore = client.ContentStore()
		ctx          = namespaces.WithNamespace(sb.Context(), "buildkit")
	)

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	// Prepare stargz registry cache
	orgImage := "docker.io/library/alpine:latest"
	sgzCache := registry + "/stargz/alpinecache:" + identity.NewID()
	baseDef := llb.Image(orgImage).Run(llb.Args([]string{"/bin/touch", "/foo"}))
	def, err := baseDef.Marshal(sb.Context())
	require.NoError(t, err)
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
		CacheExports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref":               sgzCache,
					"compression":       "estargz",
					"oci-mediatypes":    "true",
					"force-compression": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// clear all local state out
	ensurePruneAll(t, c, sb)
	workers.CheckFeatureCompat(t, sb, workers.FeatureCacheImport, workers.FeatureDirectPush)

	// stargz layers should be lazy even for executing something on them
	def, err = baseDef.
		Run(llb.Args([]string{"sh", "-c", "cat /dev/urandom | head -c 100 | sha256sum > unique"})).
		Marshal(sb.Context())
	require.NoError(t, err)
	target := registry + "/buildkit/testlazyimage:" + identity.NewID()
	resp, err := c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target,
					"push":                                   "true",
					"store":                                  "true",
					"oci-mediatypes":                         "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzCache,
				},
			},
		},
		CacheExports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref":            sgzCache,
					"compression":    "estargz",
					"oci-mediatypes": "true",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	unique, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst, "/unique")
	require.NoError(t, err)

	img, err := imageService.Get(ctx, target)
	require.NoError(t, err)

	manifest, err := images.Manifest(ctx, contentStore, img.Target, nil)
	require.NoError(t, err)

	// Check if image layers are lazy.
	// The topmost(last) layer created by `Run` isn't lazy so we skip the check for the layer.
	var sgzLayers []ocispecs.Descriptor
	for i, layer := range manifest.Layers[:len(manifest.Layers)-1] {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.ErrorIs(t, err, cerrdefs.ErrNotFound, "unexpected error %v on layer %+v (%d)", err, layer, i)
		sgzLayers = append(sgzLayers, layer)
	}
	require.NotEqual(t, 0, len(sgzLayers), "no layer can be used for checking lazypull")

	// The topmost(last) layer created by `Run` shouldn't be lazy
	_, err = contentStore.Info(ctx, manifest.Layers[len(manifest.Layers)-1].Digest)
	require.NoError(t, err)

	// Run build again and check if cache is reused
	resp, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type: ExporterImage,
				Attrs: map[string]string{
					"name":                                   target,
					"push":                                   "true",
					"store":                                  "true",
					"oci-mediatypes":                         "true",
					"unsafe-internal-store-allow-incomplete": "true",
				},
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzCache,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	dgst2, ok := resp.ExporterResponse[exptypes.ExporterImageDigestKey]
	require.Equal(t, true, ok)

	unique2, err := readFileInImage(sb.Context(), t, c, target+"@"+dgst2, "/unique")
	require.NoError(t, err)

	require.Equal(t, dgst, dgst2)
	require.Equal(t, unique, unique2)

	// clear all local state out
	err = imageService.Delete(ctx, img.Name, images.SynchronousDelete())
	require.NoError(t, err)
	checkAllReleasable(t, c, sb, true)

	// stargz layers should be exportable
	out = filepath.Join(destDir, "out2.tar")
	outW, err = os.Create(out)
	require.NoError(t, err)
	_, err = c.Solve(sb.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:   ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
		CacheImports: []CacheOptionsEntry{
			{
				Type: "registry",
				Attrs: map[string]string{
					"ref": sgzCache,
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	// Check if image layers are un-lazied
	for _, layer := range sgzLayers {
		_, err = contentStore.Info(ctx, layer.Digest)
		require.NoError(t, err)
	}

	ensurePruneAll(t, c, sb)
}
