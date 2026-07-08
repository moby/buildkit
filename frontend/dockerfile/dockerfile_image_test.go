package dockerfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testPullScratch(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test is only for containerd worker")
	}

	dockerfile := []byte(`
FROM scratch
LABEL foo=bar
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "docker.io/moby/testpullscratch:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dockerfile = []byte(`
FROM docker.io/moby/testpullscratch:latest
LABEL bar=baz
COPY foo .
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("foo-contents"), 0600),
	)

	target = "docker.io/moby/testpullscratch2:latest"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	ctr, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer ctr.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := ctr.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, ctr.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, ctr.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.Equal(t, "layers", ociimg.RootFS.Type)
	require.Equal(t, 1, len(ociimg.RootFS.DiffIDs))
	v, ok := ociimg.Config.Labels["foo"]
	require.True(t, ok)
	require.Equal(t, "bar", v)
	v, ok = ociimg.Config.Labels["bar"]
	require.True(t, ok)
	require.Equal(t, "baz", v)

	echo := llb.Image("busybox").
		Run(llb.Shlex(`sh -c "echo -n foo0 > /empty/foo"`)).
		AddMount("/empty", llb.Image("docker.io/moby/testpullscratch:latest"))

	def, err := echo.Marshal(sb.Context())
	require.NoError(t, err)

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), def, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "foo0", string(dt))
}

func testDockerfileScratchConfig(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test requires containerd worker")
	}

	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)
	dockerfile := []byte(`
FROM scratch
ENV foo=bar
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	target := "example.com/moby/dockerfilescratch:test"
	cmd := sb.Cmd(args + " --output type=image,name=" + target)
	err := cmd.Run()
	require.NoError(t, err)

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, client.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.NotEqual(t, "", ociimg.OS)
	require.NotEqual(t, "", ociimg.Architecture)
	require.NotEqual(t, "", ociimg.Config.WorkingDir)
	require.Equal(t, "layers", ociimg.RootFS.Type)
	require.Equal(t, 0, len(ociimg.RootFS.DiffIDs))

	require.Equal(t, 1, len(ociimg.History))
	require.Contains(t, ociimg.History[0].CreatedBy, "ENV foo=bar")
	require.Equal(t, true, ociimg.History[0].EmptyLayer)

	require.Contains(t, ociimg.Config.Env, "foo=bar")
	require.Condition(t, func() bool {
		for _, env := range ociimg.Config.Env {
			if strings.HasPrefix(env, "PATH=") {
				return true
			}
		}
		return false
	})
}

// moby/buildkit#5572
func testOCILayoutMultiname(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")

	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
COPY <<EOF /foo
hello
EOF
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	dest := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterOCI,
				OutputDir: dest,
				Attrs: map[string]string{
					"tar":  "false",
					"name": "org/repo:tag1,org/repo:tag2",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	var idx ocispecs.Index
	dt, err := os.ReadFile(filepath.Join(dest, "index.json"))
	require.NoError(t, err)

	err = json.Unmarshal(dt, &idx)
	require.NoError(t, err)

	validateIdx := func(idx ocispecs.Index) {
		require.Equal(t, 2, len(idx.Manifests))

		require.Equal(t, idx.Manifests[0].Digest, idx.Manifests[1].Digest)
		require.Equal(t, idx.Manifests[0].Platform, idx.Manifests[1].Platform)
		require.Equal(t, idx.Manifests[0].MediaType, idx.Manifests[1].MediaType)
		require.Equal(t, idx.Manifests[0].Size, idx.Manifests[1].Size)

		require.Equal(t, "docker.io/org/repo:tag1", idx.Manifests[0].Annotations["io.containerd.image.name"])
		require.Equal(t, "docker.io/org/repo:tag2", idx.Manifests[1].Annotations["io.containerd.image.name"])

		require.Equal(t, "tag1", idx.Manifests[0].Annotations["org.opencontainers.image.ref.name"])
		require.Equal(t, "tag2", idx.Manifests[1].Annotations["org.opencontainers.image.ref.name"])
	}
	validateIdx(idx)

	// test that tar variant matches
	buf := &bytes.Buffer{}
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
				Output: fixedWriteCloser(&iohelper.NopWriteCloser{Writer: buf}),
				Attrs: map[string]string{
					"name": "org/repo:tag1,org/repo:tag2",
				},
			},
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(buf.Bytes(), false)
	require.NoError(t, err)

	var idx2 ocispecs.Index
	err = json.Unmarshal(m["index.json"].Data, &idx2)
	require.NoError(t, err)

	validateIdx(idx2)
}

func testMultiNilRefsOCIExporter(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureMultiPlatform, workers.FeatureOCIExporter)

	f := getFrontend(t, sb)

	dockerfile := []byte(`FROM scratch`)

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
		FrontendAttrs: map[string]string{
			"platform": "linux/arm64,linux/amd64",
		},
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterOCI,
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

	require.Equal(t, 1, len(idx.Manifests))
	mlistHex := idx.Manifests[0].Digest.Hex()

	idx = ocispecs.Index{}
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mlistHex].Data, &idx)
	require.NoError(t, err)

	require.Equal(t, 2, len(idx.Manifests))
}
