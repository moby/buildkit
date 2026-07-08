package dockerfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testCmdShell(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test is only for containerd worker")
	}

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
CMD ["test"]
`,
		`
FROM nanoserver
CMD ["test"]
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "docker.io/moby/cmdoverridetest:latest"
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

	dockerfile = fmt.Appendf(nil, `
FROM %s
SHELL ["ls"]
ENTRYPOINT my entrypoint
`, target)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	target = "docker.io/moby/cmdoverridetest2:latest"
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

	require.Equal(t, []string(nil), ociimg.Config.Cmd)
	require.Equal(t, []string{"ls", "my entrypoint"}, ociimg.Config.Entrypoint)
}

func testDefaultShellAndPath(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Cross-platform multi-arch builds with scratch not fully supported on Windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureOCIExporter)
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
			"platform": "windows/amd64,linux/amd64",
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

	mlistHex := idx.Manifests[0].Digest.Hex()

	idx = ocispecs.Index{}
	err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mlistHex].Data, &idx)
	require.NoError(t, err)

	require.Equal(t, 2, len(idx.Manifests))

	for i, exp := range []struct {
		p          string
		entrypoint []string
		env        []string
	}{
		// we don't set PATH on Windows. #5445
		{p: "windows/amd64", entrypoint: []string{"cmd", "/S", "/C", "foo bar"}, env: []string(nil)},
		{p: "linux/amd64", entrypoint: []string{"/bin/sh", "-c", "foo bar"}, env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}},
	} {
		t.Run(exp.p, func(t *testing.T) {
			require.Equal(t, exp.p, platforms.Format(*idx.Manifests[i].Platform))

			var mfst ocispecs.Manifest
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+idx.Manifests[i].Digest.Hex()].Data, &mfst)
			require.NoError(t, err)

			require.Equal(t, 1, len(mfst.Layers))

			var img ocispecs.Image
			err = json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &img)
			require.NoError(t, err)

			require.Equal(t, exp.entrypoint, img.Config.Entrypoint)
			require.Equal(t, exp.env, img.Config.Env)
		})
	}
}

func testDefaultPathEnvOnWindows(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "!windows")

	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM nanoserver
USER ContainerAdministrator
RUN echo %PATH% > env_path.txt
`)
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
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

	dt, err := os.ReadFile(filepath.Join(destDir, "env_path.txt"))
	require.NoError(t, err)

	envPath := string(dt)
	require.Contains(t, envPath, "C:\\Windows")
}

func testIgnoreEntrypoint(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
ENTRYPOINT ["/nosuchcmd"]
RUN ["ls"]
`,
		`
FROM nanoserver AS build
ENTRYPOINT ["nosuchcmd.exe"]
RUN dir
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testInvalidJSONCommands(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM alpine
RUN ["echo", "hello"]this is invalid
`,
		`
FROM nanoserver
RUN ["echo", "hello"]this is invalid
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "this is invalid")

	workers.CheckFeatureCompat(t, sb,
		workers.FeatureDirectPush,
	)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testexportdf:multi"

	dockerfile = []byte(integration.UnixOrWindows(
		`
FROM alpine
ENTRYPOINT []random string
`,
		`
FROM nanoserver
ENTRYPOINT []random string
`,
	))

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterImage,
				Attrs: map[string]string{
					"name": target,
					"push": "true",
				},
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)

	require.Len(t, imgs.Images, 1)
	img := imgs.Images[0].Img

	entrypoint := integration.UnixOrWindows(
		[]string{"/bin/sh", "-c", "[]random string"},
		[]string{"cmd", "/S", "/C", "[]random string"},
	)
	require.Equal(t, entrypoint, img.Config.Entrypoint)
}

func testDockerfileInvalidCommand(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)
	dockerfile := []byte(integration.UnixOrWindows(
		`
	FROM busybox
	RUN invalidcmd
`,
		`
	FROM nanoserver
	RUN invalidcmd
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	cmd := sb.Cmd(args)
	stdout := new(bytes.Buffer)
	cmd.Stderr = stdout
	err := cmd.Run()
	require.Error(t, err)
	require.Contains(t, stdout.String(), integration.UnixOrWindows(
		"/bin/sh -c invalidcmd",
		"cmd /S /C invalidcmd",
	))
	require.Contains(t, stdout.String(), integration.UnixOrWindows(
		"did not complete successfully",
		"'invalidcmd' is not recognized as an internal or external command",
	))
}

func testDockerfileInvalidInstruction(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
FNTRYPOINT ["/bin/sh", "-c", "echo invalidinstruction"]
`,
		`
FROM nanoserver
FNTRYPOINT ["cmd", "/c", "echo invalidinstruction"]
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown instruction: FNTRYPOINT")
	require.Contains(t, err.Error(), "did you mean ENTRYPOINT?")
}
