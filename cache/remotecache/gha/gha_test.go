package gha

import (
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
)

func init() {
	if workers.IsTestDockerd() {
		workers.InitDockerdWorker()
	} else {
		workers.InitOCIWorker()
		workers.InitContainerdWorker()
	}
}

func TestGhaCacheIntegration(t *testing.T) {
	integration.Run(t,
		integration.TestFuncs(testBasicGhaCacheImportExportExtraTimeout),
		integration.WithMirroredImages(integration.OfficialImages("busybox:latest")),
	)
}

func testBasicGhaCacheImportExportExtraTimeout(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	workers.CheckFeatureCompat(t, sb,
		workers.FeatureCacheExport,
		workers.FeatureCacheImport,
		workers.FeatureCacheBackendGha,
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	busybox := llb.Image("busybox:latest")
	st := llb.Scratch()

	run := func(cmd string) {
		st = busybox.Run(llb.Shlex(cmd), llb.Dir("/wd")).AddMount("/wd", st)
	}

	run(`sh -c "echo -n foobar > const"`)
	run(`sh -c "cat /dev/urandom | head -c 100 | sha256sum > unique"`)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	var cacheVersion string
	if v, ok := os.LookupEnv("ACTIONS_CACHE_SERVICE_V2"); ok {
		if b, err := strconv.ParseBool(v); err == nil && b {
			cacheVersion = "2"
		}
	}

	cacheAttrs := map[string]string{}
	if cacheVersion == "2" {
		cacheAttrs["url_v2"] = os.Getenv("ACTIONS_RESULTS_URL")
	}
	cacheAttrs["url"] = os.Getenv("ACTIONS_CACHE_URL")
	if cacheAttrs["url"] == "" {
		cacheAttrs["url"] = os.Getenv("ACTIONS_RESULTS_URL")
	}
	cacheAttrs["token"] = os.Getenv("ACTIONS_RUNTIME_TOKEN")

	if cacheAttrs["token"] == "" || (cacheAttrs["url"] == "" && cacheAttrs["url_v2"] == "") {
		t.Skip("actions runtime token and cache url must be set")
	}

	scope := "buildkit-" + t.Name()
	if ref := os.Getenv("GITHUB_REF"); ref != "" {
		if after, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			scope += "-" + after
		} else if after, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
			scope += "-" + after
		} else if strings.HasPrefix(ref, "refs/pull/") {
			scope += "-pr" + strings.TrimPrefix(strings.TrimSuffix(strings.TrimSuffix(ref, "/head"), "/merge"), "refs/pull/")
		}
	}

	cacheExportAttrs := map[string]string{
		"scope": scope,
		"mode":  "max",
	}
	maps.Copy(cacheExportAttrs, cacheAttrs)

	_, err = c.Solve(sb.Context(), def, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheExports: []client.CacheOptionsEntry{{
			Type:  "gha",
			Attrs: cacheExportAttrs,
		}},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	ensurePruneAll(t, c, sb)

	destDir = t.TempDir()

	cacheImportAttrs := map[string]string{
		"scope": scope,
	}
	maps.Copy(cacheImportAttrs, cacheAttrs)

	// Github Cache Service v2 has a problem of not being immediately consistent
	// so we need to wait for the changes to propagate.
	time.Sleep(3 * time.Second)

	_, err = c.Solve(sb.Context(), def, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		CacheImports: []client.CacheOptionsEntry{{
			Type:  "gha",
			Attrs: cacheImportAttrs,
		}},
	}, nil)
	require.NoError(t, err)

	dt2, err := os.ReadFile(filepath.Join(destDir, "const"))
	require.NoError(t, err)
	require.Equal(t, "foobar", string(dt2))

	dt2, err = os.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)
	require.Equal(t, string(dt), string(dt2))
}

func ensurePruneAll(t *testing.T, c *client.Client, sb integration.Sandbox) {
	for i := range 2 {
		require.NoError(t, c.Prune(sb.Context(), nil, client.PruneAll))
		for range 20 {
			du, err := c.DiskUsage(sb.Context())
			require.NoError(t, err)
			if len(du) == 0 {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Logf("retrying prune(%d)", i)
	}
	t.Fatalf("failed to ensure prune")
}

func requiresLinux(t *testing.T) {
	integration.SkipOnPlatform(t, "!linux")
}
