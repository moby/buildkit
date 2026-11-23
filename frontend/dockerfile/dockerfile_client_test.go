package dockerfile

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func testGatewayBuiltinSyntaxSourceDockerfileV0(t *testing.T, sb integration.Sandbox) {
	if _, ok := getFrontend(t, sb).(*builtinFrontend); !ok {
		t.Skip()
	}

	dockerfile := []byte(integration.UnixOrWindows(
		`
# syntax=docker/dockerfile-upstream:master
FROM busybox AS build
RUN echo -n ok > /out
FROM scratch
COPY --from=build /out /out
`,
		`
# syntax=docker/dockerfile-upstream:master
FROM nanoserver:latest AS build
USER ContainerAdministrator
RUN echo ok>C:\out
FROM nanoserver:latest
USER ContainerAdministrator
COPY --from=build /out /out
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = c.Solve(sb.Context(), nil, client.SolveOpt{
		Frontend: "gateway.v0",
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
		FrontendAttrs: map[string]string{
			"source":  "dockerfile.v0",
			"cmdline": "dockerfile.v0",
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "out"))
	require.NoError(t, err)
	require.Equal(t, "ok", strings.TrimSpace(string(dt)))
}

func testFrontendUseForwardedSolveResults(t *testing.T, sb integration.Sandbox) {
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfileStr := `
FROM %s
COPY foo foo2
`
	dockerfile := fmt.Appendf(nil, dockerfileStr, integration.UnixOrWindows("scratch", "nanoserver"))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("data"), 0600),
	)

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
		})
		if err != nil {
			return nil, err
		}

		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}

		st2, err := ref.ToState()
		if err != nil {
			return nil, err
		}

		st := llb.Scratch().File(
			llb.Copy(st2, "foo2", "foo3"),
		)

		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	destDir := t.TempDir()

	_, err = c.Build(sb.Context(), client.SolveOpt{
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
	}, "", frontend, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "foo3"))
	require.NoError(t, err)
	require.Equal(t, dt, []byte("data"))
}

func testFrontendEvaluate(t *testing.T, sb integration.Sandbox) {
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY badfile /
`,
		`
FROM nanoserver
COPY badfile /
`,
	))
	dir := integration.Tmpdir(t, fstest.CreateFile("Dockerfile", dockerfile, 0600))

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		_, err := c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			Evaluate: true,
		})
		require.ErrorContains(t, err, `"/badfile": not found`)

		platformOpt := integration.UnixOrWindows("linux/amd64,linux/arm64", "windows/amd64")
		_, err = c.Solve(ctx, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendOpt: map[string]string{
				"platform": platformOpt,
			},
			Evaluate: true,
		})
		require.ErrorContains(t, err, `"/badfile": not found`)

		return nil, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		Exports: []client.ExportEntry{},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, "", frontend, nil)
	require.NoError(t, err)
}

func testFrontendResolveNever(t *testing.T, sb integration.Sandbox) {
	f, ok := getFrontend(t, sb).(*gatewayFrontend)
	if !ok {
		t.Skip("requires an external Dockerfile frontend")
	}
	integration.SkipOnPlatform(t, "windows")
	ctx := sb.Context()
	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dir := integration.Tmpdir(t,
		fstest.CreateFile("Dockerfile", []byte("FROM scratch\nCOPY data /data\n"), 0o600),
		fstest.CreateFile("data", []byte("offline frontend"), 0o600),
	)
	opt := client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
		FrontendAttrs: map[string]string{"image-resolve-mode": "never"},
	}
	_, err = f.Solve(ctx, c, opt, nil)
	require.ErrorContains(t, err, "pulling is disabled")
	opt.FrontendAttrs = map[string]string{}
	_, err = f.Solve(ctx, c, opt, nil)
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		syntax string
		attrs  map[string]string
	}{
		{
			name:   "syntax",
			syntax: f.gw,
		},
		{
			name:   "syntax-arg",
			syntax: "example.com/unused:latest",
			attrs:  map[string]string{"build-arg:BUILDKIT_SYNTAX": f.gw},
		},
		{
			name:   "named-context",
			syntax: "example.com/frontend:latest",
			attrs:  map[string]string{"context:example.com/frontend": "docker-image://" + f.gw},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := integration.Tmpdir(t,
				fstest.CreateFile("Dockerfile", []byte("#syntax="+tc.syntax+"\nFROM scratch\nCOPY data /data\n"), 0o600),
				fstest.CreateFile("data", []byte("offline frontend"), 0o600),
			)
			attrs := map[string]string{"image-resolve-mode": "never"}
			maps.Copy(attrs, tc.attrs)
			dest := t.TempDir()
			_, err := c.Solve(ctx, nil, client.SolveOpt{
				Frontend:      "dockerfile.v0",
				FrontendAttrs: attrs,
				LocalMounts: map[string]fsutil.FS{
					dockerui.DefaultLocalNameDockerfile: dir,
					dockerui.DefaultLocalNameContext:    dir,
				},
				Exports: []client.ExportEntry{{Type: client.ExporterLocal, OutputDir: dest}},
			}, nil)
			require.NoError(t, err)
			dt, err := os.ReadFile(filepath.Join(dest, "data"))
			require.NoError(t, err)
			require.Equal(t, "offline frontend", string(dt))
		})
	}
	ensurePruneAll(t, c, sb)
	opt.FrontendAttrs["image-resolve-mode"] = "never"
	_, err = f.Solve(ctx, c, opt, nil)
	require.ErrorContains(t, err, "pulling is disabled")
	delete(opt.FrontendAttrs, "image-resolve-mode")
	_, err = f.Solve(ctx, c, opt, nil)
	require.NoError(t, err)
}

func testFrontendInputs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	baseImage := integration.UnixOrWindows("busybox", "nanoserver:latest")
	cmd := integration.UnixOrWindows(
		`sh -c "cat /dev/urandom | head -c 100 | sha256sum > /out/foo"`,
		`cmd /c "echo %RANDOM%%RANDOM%%RANDOM%%RANDOM% > C:\\out\\foo"`,
	)

	mountPath := integration.UnixOrWindows("/out", "C:/out")

	outMount := llb.Image(baseImage).Run(llb.Shlex(cmd)).AddMount(mountPath, llb.Scratch())

	def, err := outMount.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	require.NoError(t, err)

	expected, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)

	dockerfile := integration.UnixOrWindows([]byte(`
FROM scratch
COPY foo foo2
`), []byte(`
FROM nanoserver:latest
COPY foo foo2
`))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
		},
		FrontendInputs: map[string]llb.State{
			dockerui.DefaultLocalNameContext: outMount,
		},
	}, nil)
	require.NoError(t, err)

	actual, err := os.ReadFile(filepath.Join(destDir, "foo2"))
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func testFrontendSubrequests(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	if _, ok := f.(*clientFrontend); !ok {
		t.Skip("only test with client frontend")
	}

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY Dockerfile Dockerfile
`,
		`
FROM nanoserver
COPY Dockerfile Dockerfile
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	called := false

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		reqs, err := subrequests.Describe(ctx, c)
		require.NoError(t, err)

		require.Greater(t, len(reqs), 0)

		hasDescribe := false

		for _, req := range reqs {
			if req.Name == "frontend.subrequests.describe" {
				hasDescribe = true
				require.Equal(t, subrequests.RequestType("rpc"), req.Type)
				require.NotEqual(t, "", req.Version)
				require.Greater(t, len(req.Metadata), 0)
				require.Equal(t, "result.json", req.Metadata[0].Name)
			}
		}
		require.True(t, hasDescribe)

		_, err = c.Solve(ctx, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"requestid":     "frontend.subrequests.notexist",
				"frontend.caps": "moby.buildkit.frontend.subrequests",
			},
			Frontend: "dockerfile.v0",
		})
		require.Error(t, err)
		var reqErr *errdefs.UnsupportedSubrequestError
		require.ErrorAs(t, err, &reqErr)
		require.Equal(t, "frontend.subrequests.notexist", reqErr.GetName())

		_, err = c.Solve(ctx, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"frontend.caps": "moby.buildkit.frontend.notexistcap",
			},
			Frontend: "dockerfile.v0",
		})
		require.Error(t, err)
		var capErr *errdefs.UnsupportedFrontendCapError
		require.ErrorAs(t, err, &capErr)
		require.Equal(t, "moby.buildkit.frontend.notexistcap", capErr.GetName())

		called = true
		return nil, nil
	}

	_, err = c.Build(sb.Context(), client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
		},
	}, "", frontend, nil)
	require.NoError(t, err)

	require.True(t, called)
}

func testNilContextInSolveGateway(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Build(sb.Context(), client.SolveOpt{}, "", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := f.SolveGateway(ctx, c, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendInputs: map[string]*pb.Definition{
				dockerui.DefaultLocalNameContext:    nil,
				dockerui.DefaultLocalNameDockerfile: nil,
			},
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}, nil)
	// should not cause buildkitd to panic
	require.ErrorContains(t, err, "invalid nil input definition to definition op")
}

func testMultiNilRefsInSolveGateway(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureMultiPlatform)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	_, err = c.Build(sb.Context(), client.SolveOpt{}, "", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		localDockerfile, err := llb.Scratch().
			File(llb.Mkfile("Dockerfile", 0644, []byte(`FROM scratch`))).
			Marshal(ctx)
		if err != nil {
			return nil, err
		}

		res, err := f.SolveGateway(ctx, c, gateway.SolveRequest{
			Frontend: "dockerfile.v0",
			FrontendOpt: map[string]string{
				"platform": "linux/amd64,linux/arm64",
			},
			FrontendInputs: map[string]*pb.Definition{
				dockerui.DefaultLocalNameDockerfile: localDockerfile.ToPB(),
			},
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}, nil)
	require.NoError(t, err)
}
