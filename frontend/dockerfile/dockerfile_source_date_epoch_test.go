package dockerfile

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testSourceDateEpochWithoutExporter(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
ENTRYPOINT foo bar
COPY Dockerfile .
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	tm := time.Unix(1700000001, 0).UTC()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				// disable exporter epoch to make sure we test dockerfile
				Attrs:  map[string]string{"source-date-epoch": ""},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out.tar"))
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var idx ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &idx)
	require.NoError(t, err)

	mlistHex := idx.Manifests[0].Digest.Hex()

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mlistHex].Data, &mfst)
	require.NoError(t, err)

	var img ocispecs.Image
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &img)
	require.NoError(t, err)

	require.Equal(t, tm.Unix(), img.Created.Unix())
	for _, h := range img.History {
		require.Equal(t, tm.Unix(), h.Created.Unix())
	}
}

func testSourceDateEpochDockerfileDefault(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)
	dockerfile := fmt.Appendf(nil, `
ARG SOURCE_DATE_EPOCH=%d
FROM scratch
COPY Dockerfile .
`, tm.Unix())

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	mfst := readOCIManifest(t, dt)
	require.Len(t, mfst.Layers, 1)
	require.Equal(t, fmt.Sprintf("%d", tm.Unix()), mfst.Layers[0].Annotations["buildkit/rewritten-timestamp"])
}

func testSourceDateEpochDockerfileDefaultOverride(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	defaultTM := time.Unix(1700000001, 0).UTC()
	overrideTM := time.Unix(1700000002, 0).UTC()
	dockerfile := fmt.Appendf(nil, `
ARG SOURCE_DATE_EPOCH=%d
FROM scratch
COPY Dockerfile .
`, defaultTM.Unix())

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", overrideTM.Unix()),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	mfst := readOCIManifest(t, dt)
	require.Len(t, mfst.Layers, 1)
	require.Equal(t, fmt.Sprintf("%d", overrideTM.Unix()), mfst.Layers[0].Annotations["buildkit/rewritten-timestamp"])
}

func testSourceDateEpochDockerfileDefaultReset(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	tm := time.Date(2015, time.October, 21, 7, 28, 0, 0, time.UTC)
	dockerfile := fmt.Appendf(nil, `
ARG SOURCE_DATE_EPOCH=%d
FROM scratch
COPY Dockerfile .
`, tm.Unix())

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"source-date-epoch": "",
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	mfst := readOCIManifest(t, dt)
	require.Len(t, mfst.Layers, 1)
	require.Empty(t, mfst.Layers[0].Annotations["buildkit/rewritten-timestamp"])
}

func testSourceDateEpochDockerfileDefaultInvalid(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
ARG SOURCE_DATE_EPOCH=not-a-timestamp
FROM scratch
COPY Dockerfile .
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()
	out := filepath.Join(destDir, "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.ErrorContains(t, err, "invalid SOURCE_DATE_EPOCH: not-a-timestamp")
}

func testSourceDateEpochContextGit(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	gitDir := t.TempDir()
	dockerfile := []byte("FROM scratch\nCOPY Dockerfile /\n")
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "Dockerfile"), dockerfile, 0600))

	commitTime := time.Unix(1700000101, 0).UTC()
	require.NoError(t, runShell(gitDir,
		"git init",
		"git config --local user.email test@example.com",
		"git config --local user.name test",
		"git add Dockerfile",
		fmt.Sprintf("GIT_AUTHOR_DATE=%q GIT_COMMITTER_DATE=%q git commit -m msg", commitTime.Format(time.RFC3339), commitTime.Format(time.RFC3339)),
		"git update-server-info",
	))

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Clean(gitDir))))
	defer server.Close()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":                     server.URL + "/.git",
			"build-arg:SOURCE_DATE_EPOCH": "context",
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, commitTime.Unix(), readOCIImageCreated(t, dt).Unix())
}

func testSourceDateEpochContextHTTPLastModified(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	lastModified := time.Unix(1700000201, 0).UTC()
	archiveMaxTime := time.Unix(1700000202, 0).UTC()
	resp := &httpserver.Response{
		Etag:         identity.NewID(),
		LastModified: &lastModified,
		Content: makeTarContext(t,
			tarContextFile{name: "Dockerfile", data: []byte("FROM scratch\nCOPY foo /\n"), modTime: archiveMaxTime.Add(-time.Hour)},
			tarContextFile{name: "foo", data: []byte("bar"), modTime: archiveMaxTime},
		),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/context.tar": resp,
	})
	defer server.Close()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":                     server.URL + "/context.tar",
			"build-arg:SOURCE_DATE_EPOCH": "context",
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, lastModified.Unix(), readOCIImageCreated(t, dt).Unix())
}

func testSourceDateEpochContextHTTPArchive(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	archiveMaxTime := time.Unix(1700000302, 0).UTC()
	resp := &httpserver.Response{
		Etag: identity.NewID(),
		Content: makeTarContext(t,
			tarContextFile{name: "Dockerfile", data: []byte("FROM busybox\nARG SOURCE_DATE_EPOCH\nRUN echo -n \"$SOURCE_DATE_EPOCH\" >/epoch\n"), modTime: archiveMaxTime.Add(-time.Hour)},
			tarContextFile{name: "foo", data: []byte("bar"), modTime: archiveMaxTime},
		),
	}
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/context.tar": resp,
	})
	defer server.Close()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context":                     server.URL + "/context.tar",
			"build-arg:SOURCE_DATE_EPOCH": "context",
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	mfst := readOCIManifest(t, dt)
	require.NotEmpty(t, mfst.Layers)
	require.Equal(t, fmt.Sprintf("%d", archiveMaxTime.Unix()), mfst.Layers[len(mfst.Layers)-1].Annotations["buildkit/rewritten-timestamp"])

	layerMap := readOCILayerMap(t, dt, mfst.Layers[len(mfst.Layers)-1])
	require.Equal(t, fmt.Sprintf("%d", archiveMaxTime.Unix()), string(layerMap["epoch"].Data))

	require.Equal(t, archiveMaxTime.Unix(), readOCIImageCreated(t, dt).Unix())
}

func testSourceDateEpochContextLocalUnset(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	dockerfile := []byte("FROM scratch\nCOPY Dockerfile /\n")
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": "context",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)
	mfst := readOCIManifest(t, dt)
	require.Len(t, mfst.Layers, 1)
	require.Empty(t, mfst.Layers[0].Annotations["buildkit/rewritten-timestamp"])
}

func testSourceDateEpochStageHTTPArchive(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	archiveMaxTime := time.Unix(1700000402, 0).UTC()
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/src.tar": {
			Etag: identity.NewID(),
			Content: makeTarContext(t,
				tarContextFile{name: "foo", data: []byte("bar"), modTime: archiveMaxTime},
			),
		},
	})
	defer server.Close()

	dockerfile := fmt.Appendf(nil, `
ARG SOURCE_DATE_EPOCH=mysource
FROM scratch AS mysource
ADD %s/src.tar /
FROM busybox
ARG SOURCE_DATE_EPOCH
RUN echo -n "$SOURCE_DATE_EPOCH" >/epoch
`, server.URL)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	mfst := readOCIManifest(t, dt)
	require.NotEmpty(t, mfst.Layers)
	require.Equal(t, fmt.Sprintf("%d", archiveMaxTime.Unix()), mfst.Layers[len(mfst.Layers)-1].Annotations["buildkit/rewritten-timestamp"])

	layerMap := readOCILayerMap(t, dt, mfst.Layers[len(mfst.Layers)-1])
	require.Equal(t, fmt.Sprintf("%d", archiveMaxTime.Unix()), string(layerMap["epoch"].Data))
	require.Equal(t, archiveMaxTime.Unix(), readOCIImageCreated(t, dt).Unix())
}

func testSourceDateEpochStageOverride(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	defaultTime := time.Unix(1700000501, 0).UTC()
	overrideTime := time.Unix(1700000502, 0).UTC()
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/src.tar": {
			Etag: identity.NewID(),
			Content: makeTarContext(t,
				tarContextFile{name: "foo", data: []byte("bar"), modTime: defaultTime},
			),
		},
	})
	defer server.Close()

	dockerfile := fmt.Appendf(nil, `
ARG SOURCE_DATE_EPOCH=mysource
FROM scratch AS mysource
ADD %s/src.tar /
FROM scratch
COPY Dockerfile .
`, server.URL)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", overrideTime.Unix()),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)
	mfst := readOCIManifest(t, dt)
	require.Len(t, mfst.Layers, 1)
	require.Equal(t, fmt.Sprintf("%d", overrideTime.Unix()), mfst.Layers[0].Annotations["buildkit/rewritten-timestamp"])
}

func testSourceDateEpochStageInvalid(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
ARG SOURCE_DATE_EPOCH=mysource
FROM scratch AS mysource
COPY Dockerfile /Dockerfile
FROM scratch
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.ErrorContains(t, err, "SOURCE_DATE_EPOCH stage does not meet source-only requirements")
}

func testSourceDateEpochNamedContextHTTPLastModified(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	lastModified := time.Unix(1700000601, 0).UTC()
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/src.tar": {
			Etag:         identity.NewID(),
			LastModified: &lastModified,
			Content: makeTarContext(t,
				tarContextFile{name: "foo", data: []byte("bar"), modTime: lastModified.Add(time.Hour)},
			),
		},
	})
	defer server.Close()

	dockerfile := []byte(`
ARG SOURCE_DATE_EPOCH=mysource
FROM scratch
COPY Dockerfile .
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:mysource": server.URL + "/src.tar",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)
	mfst := readOCIManifest(t, dt)
	require.Len(t, mfst.Layers, 1)
	require.Equal(t, fmt.Sprintf("%d", lastModified.Unix()), mfst.Layers[0].Annotations["buildkit/rewritten-timestamp"])
	require.Equal(t, lastModified.Unix(), readOCIImageCreated(t, dt).Unix())
}

func testSourceDateEpochNamedContextHTTPArchive(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	f := getFrontend(t, sb)

	archiveMaxTime := time.Unix(1700000602, 0).UTC()
	server := httpserver.NewTestServer(map[string]*httpserver.Response{
		"/src.tar": {
			Etag: identity.NewID(),
			Content: makeTarContext(t,
				tarContextFile{name: "foo", data: []byte("bar"), modTime: archiveMaxTime},
			),
		},
	})
	defer server.Close()

	dockerfile := []byte(`
ARG SOURCE_DATE_EPOCH=mysource
FROM busybox
ARG SOURCE_DATE_EPOCH
RUN echo -n "$SOURCE_DATE_EPOCH" >/epoch
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	out := filepath.Join(t.TempDir(), "out.tar")
	outW, err := os.Create(out)
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"context:mysource": server.URL + "/src.tar",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Attrs: map[string]string{
					"rewrite-timestamp": "true",
				},
				Output: fixedWriteCloser(outW),
			},
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(out)
	require.NoError(t, err)

	mfst := readOCIManifest(t, dt)
	require.NotEmpty(t, mfst.Layers)
	require.Equal(t, fmt.Sprintf("%d", archiveMaxTime.Unix()), mfst.Layers[len(mfst.Layers)-1].Annotations["buildkit/rewritten-timestamp"])

	layerMap := readOCILayerMap(t, dt, mfst.Layers[len(mfst.Layers)-1])
	require.Equal(t, fmt.Sprintf("%d", archiveMaxTime.Unix()), string(layerMap["epoch"].Data))
	require.Equal(t, archiveMaxTime.Unix(), readOCIImageCreated(t, dt).Unix())
}

func testReproSourceDateEpoch(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "COPY --link requires diffApply which is not supported on Windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter, workers.FeatureSourceDateEpoch)
	if sb.Snapshotter() == "native" {
		t.Skip("the digest is not reproducible with the \"native\" snapshotter because hardlinks are processed in a different way: https://github.com/moby/buildkit/pull/3456#discussion_r1062650263")
	}

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	f := getFrontend(t, sb)

	tm := time.Date(2023, time.January, 10, 12, 34, 56, 0, time.UTC) // 1673354096
	t.Logf("SOURCE_DATE_EPOCH=%d", tm.Unix())

	type testCase struct {
		name           string
		dockerfile     string
		files          []fstest.Applier
		expectedDigest string
		noCacheExport  bool
	}
	testCases := []testCase{
		{
			name: "Basic",
			dockerfile: `# The base image could not be busybox, due to https://github.com/moby/buildkit/issues/3455
FROM amd64/debian:bullseye-20230109-slim
RUN touch /foo
RUN touch /foo.1
RUN touch -d '2010-01-01 12:34:56' /foo-2010
RUN touch -d '2010-01-01 12:34:56' /foo-2010.1
RUN touch -d '2030-01-01 12:34:56' /foo-2030
RUN touch -d '2030-01-01 12:34:56' /foo-2030.1
RUN rm -f /foo.1
RUN rm -f /foo-2010.1
RUN rm -f /foo-2030.1
`,
			expectedDigest: "sha256:04e5d0cbee3317c79f50494cfeb4d8a728402a970ef32582ee47c62050037e3f",
		},
		{
			// https://github.com/moby/buildkit/issues/4746
			name: "CopyLink",
			dockerfile: `FROM amd64/debian:bullseye-20230109-slim
COPY --link foo foo
`,
			files:          []fstest.Applier{fstest.CreateFile("foo", []byte("foo"), 0600)},
			expectedDigest: "sha256:9f75e4bdbf3d825acb36bb603ddef4a25742afb8ccb674763ffc611ae047d8a6",
		},
		{
			// https://github.com/moby/buildkit/issues/4793
			name: "NoAdditionalLayer",
			dockerfile: `FROM amd64/debian:bullseye-20230109-slim
`,
			expectedDigest: "sha256:eeba8ef81dec46359d099c5d674009da54e088fa8f29945d4d7fb3a7a88c450e",
			noCacheExport:  true, // "skipping cache export for empty result"
		},
	}

	// https://explore.ggcr.dev/?image=amd64%2Fdebian%3Abullseye-20230109-slim
	baseImageLayers := []digest.Digest{
		"sha256:8740c948ffd4c816ea7ca963f99ca52f4788baa23f228da9581a9ea2edd3fcd7",
	}
	baseImageHistoryTimestamps := []time.Time{
		timeMustParse(t, time.RFC3339Nano, "2023-01-11T02:34:44.402266175Z"),
		timeMustParse(t, time.RFC3339Nano, "2023-01-11T02:34:44.829692296Z"),
	}

	ctx := sb.Context()
	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := integration.Tmpdir(
				t,
				append([]fstest.Applier{fstest.CreateFile("Dockerfile", []byte(tc.dockerfile), 0600)}, tc.files...)...,
			)

			target := registry + "/buildkit/testreprosourcedateepoch-" + strings.ToLower(tc.name) + ":" + fmt.Sprintf("%d", tm.Unix())
			solveOpt := client.SolveOpt{
				FrontendAttrs: map[string]string{
					"build-arg:SOURCE_DATE_EPOCH": fmt.Sprintf("%d", tm.Unix()),
					"platform":                    "linux/amd64",
				},
				LocalMounts: map[string]fsutil.FS{
					dockerui.DefaultLocalNameDockerfile: dir,
					dockerui.DefaultLocalNameContext:    dir,
				},
				Exports: []client.ExportEntry{
					{
						Type: client.ExporterImage,
						Attrs: map[string]string{
							"name":              target,
							"push":              "true",
							"oci-mediatypes":    "true",
							"rewrite-timestamp": "true",
						},
					},
				},
				CacheExports: []client.CacheOptionsEntry{
					{
						Type: "registry",
						Attrs: map[string]string{
							"ref":            target + "-cache",
							"oci-mediatypes": "true",
						},
					},
				},
			}
			_, err = f.Solve(ctx, c, solveOpt, nil)
			require.NoError(t, err)

			desc, manifest, img := readImage(t, ctx, target)
			var cacheManifest ocispecs.Manifest
			if !tc.noCacheExport {
				_, cacheManifest, _ = readImage(t, ctx, target+"-cache")
			}
			t.Log("The digest may change depending on the BuildKit version, the snapshotter configuration, etc.")
			require.Equal(t, tc.expectedDigest, desc.Digest.String())

			// Image history from the base config must remain immutable
			for i, tm := range baseImageHistoryTimestamps {
				require.True(t, img.History[i].Created.Equal(tm))
			}

			// Image layers, *except the base layers*, must have rewritten-timestamp
			for i, l := range manifest.Layers {
				if i < len(baseImageLayers) {
					require.Empty(t, l.Annotations["buildkit/rewritten-timestamp"])
					require.Equal(t, baseImageLayers[i], l.Digest)
				} else {
					require.Equal(t, fmt.Sprintf("%d", tm.Unix()), l.Annotations["buildkit/rewritten-timestamp"])
				}
			}
			if !tc.noCacheExport {
				// Cache layers must *not* have rewritten-timestamp
				for _, l := range cacheManifest.Layers {
					require.Empty(t, l.Annotations["buildkit/rewritten-timestamp"])
				}
			}

			// Build again, after pruning the base image layer cache.
			// For testing https://github.com/moby/buildkit/issues/4746
			ensurePruneAll(t, c, sb)
			_, err = f.Solve(ctx, c, solveOpt, nil)
			require.NoError(t, err)
			descAfterPrune, _, _ := readImage(t, ctx, target)
			require.Equal(t, desc.Digest.String(), descAfterPrune.Digest.String())

			// Build again, but without rewrite-timestamp
			solveOpt2 := solveOpt
			delete(solveOpt2.Exports[0].Attrs, "rewrite-timestamp")
			_, err = f.Solve(ctx, c, solveOpt2, nil)
			require.NoError(t, err)
			_, manifest2, img2 := readImage(t, ctx, target)
			for i, tm := range baseImageHistoryTimestamps {
				require.True(t, img2.History[i].Created.Equal(tm))
			}
			for _, l := range manifest2.Layers {
				require.Empty(t, l.Annotations["buildkit/rewritten-timestamp"])
			}
		})
	}
}

type tarContextFile struct {
	name    string
	data    []byte
	modTime time.Time
}

func makeTarContext(t *testing.T, files ...tarContextFile) []byte {
	t.Helper()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	for _, file := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     file.name,
			Mode:     0600,
			Size:     int64(len(file.data)),
			Typeflag: tar.TypeReg,
			ModTime:  file.modTime,
		}))
		_, err := tw.Write(file.data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func readOCIImage(t *testing.T, dt []byte) ocispecs.Image {
	t.Helper()

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var idx ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &idx)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+idx.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	var img ocispecs.Image
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &img)
	require.NoError(t, err)

	return img
}

func readOCIImageCreated(t *testing.T, dt []byte) time.Time {
	t.Helper()

	img := readOCIImage(t, dt)
	require.NotNil(t, img.Created)
	return *img.Created
}

func readOCIManifest(t *testing.T, dt []byte) ocispecs.Manifest {
	t.Helper()

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	var idx ocispecs.Index
	err = json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &idx)
	require.NoError(t, err)

	var mfst ocispecs.Manifest
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+idx.Manifests[0].Digest.Hex()].Data, &mfst)
	require.NoError(t, err)

	return mfst
}

func readOCILayerMap(t *testing.T, dt []byte, layer ocispecs.Descriptor) map[string]*testutil.TarItem {
	t.Helper()

	m, err := testutil.ReadTarToMap(dt, false)
	require.NoError(t, err)

	layerMap, err := testutil.ReadTarToMap(m[ocispecs.ImageBlobsDir+"/sha256/"+layer.Digest.Hex()].Data, true)
	require.NoError(t, err)

	return layerMap
}

func timeMustParse(t *testing.T, layout, value string) time.Time {
	tm, err := time.Parse(layout, value)
	require.NoError(t, err)
	return tm
}

//nolint:revive // context-as-argument: context.Context should be the first parameter of a function
func readImage(t *testing.T, ctx context.Context, ref string) (ocispecs.Descriptor, ocispecs.Manifest, ocispecs.Image) {
	desc, provider, err := contentutil.ProviderFromRef(ref)
	require.NoError(t, err)
	dt, err := content.ReadBlob(ctx, provider, desc)
	require.NoError(t, err)
	var manifest ocispecs.Manifest
	require.NoError(t, json.Unmarshal(dt, &manifest))
	imgDt, err := content.ReadBlob(ctx, provider, manifest.Config)
	require.NoError(t, err)
	// Verify that all the layer blobs are present
	for _, layer := range manifest.Layers {
		layerRA, err := provider.ReaderAt(ctx, layer)
		require.NoError(t, err)
		layerDigest, err := layer.Digest.Algorithm().FromReader(content.NewReader(layerRA))
		require.NoError(t, err)
		require.Equal(t, layer.Digest, layerDigest)
	}
	var img ocispecs.Image
	require.NoError(t, json.Unmarshal(imgDt, &img))
	return desc, manifest, img
}
