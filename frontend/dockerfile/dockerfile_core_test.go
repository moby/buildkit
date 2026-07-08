package dockerfile

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

func testDockerfileDirs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
COPY foo /foo2
COPY foo /
RUN echo -n bar > foo3
RUN test -f foo
RUN cmp -s foo foo2
RUN cmp -s foo foo3
`,
		`
FROM nanoserver:plus
USER ContainerAdministrator
COPY foo /foo2
COPY foo /
RUN echo bar> foo3
RUN IF EXIST foo (exit 0) ELSE (exit 1)
RUN findstr /M "bar" foo2 >nul && (exit 0) || (exit 1)
RUN findstr /M "bar" foo3 >nul && (exit 0) || (exit 1)
`,
	))

	bar := integration.UnixOrWindows(`bar`, "bar\r\n")

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(bar), 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	cmd := sb.Cmd(args)
	stdout := new(bytes.Buffer)
	cmd.Stderr = stdout
	err1 := cmd.Run()
	require.NoError(t, err1)

	_, err := os.Stat(trace)
	require.NoError(t, err)

	// relative urls
	args, trace = f.DFCmdArgs(".", ".")
	defer os.RemoveAll(trace)

	cmd = sb.Cmd(args)
	cmd.Dir = dir.Name
	stdout.Reset()
	cmd.Stderr = stdout
	err2 := cmd.Run()
	require.NoError(t, err2)

	_, err = os.Stat(trace)
	require.NoError(t, err)

	// different context and dockerfile directories
	dir1 := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	dir2 := integration.Tmpdir(
		t,
		fstest.CreateFile("foo", []byte(bar), 0600),
	)

	args, trace = f.DFCmdArgs(dir2.Name, dir1.Name)
	defer os.RemoveAll(trace)

	cmd = sb.Cmd(args)
	cmd.Dir = dir.Name
	require.NoError(t, cmd.Run())

	_, err = os.Stat(trace)
	require.NoError(t, err)

	// TODO: test trace file output, cache hits, logs etc.
	// TODO: output metadata about original dockerfile command in trace
}

func testSymlinkedDockerfile(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
ENV foo bar
`,
		`
FROM nanoserver
ENV foo bar
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile.web", dockerfile, 0600),
		fstest.Symlink("Dockerfile.web", "Dockerfile"),
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

func testSymlinkDestination(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Tests Unix symlink behavior with FROM scratch which is not well-supported on Windows")
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	expectedContent := []byte("content0")
	err := tw.WriteHeader(&tar.Header{
		Name:     "symlink",
		Typeflag: tar.TypeSymlink,
		Linkname: "../tmp/symlink-target",
		Mode:     0755,
	})
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	dockerfile := []byte(`
FROM scratch
ADD t.tar /
COPY foo /symlink/
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", expectedContent, 0600),
		fstest.CreateFile("t.tar", buf.Bytes(), 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir := t.TempDir()

	cmd := sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err := os.ReadFile(filepath.Join(destDir, "tmp/symlink-target/foo"))
	require.NoError(t, err)
	require.Equal(t, expectedContent, dt)
}

// tonistiigi/fsutil#46
func testContextChangeDirToFile(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo /
`,
		`
FROM nanoserver
COPY foo /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateDir("foo", 0700),
		fstest.CreateFile("foo/bar", []byte(`contents`), 0600),
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

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`contents2`), 0600),
	)
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

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "contents2", string(dt))
}

func testNoSnapshotLeak(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY foo /
`,
		`
FROM nanoserver
COPY foo /
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`contents`), 0600),
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

	du, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	du2, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)

	require.Equal(t, len(du), len(du2))
}

func testExposeExpansion(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
ARG PORTS="3000 4000/udp"
EXPOSE $PORTS
EXPOSE 5000
`,
		`
FROM nanoserver
ARG PORTS="3000 4000/udp"
EXPOSE $PORTS
EXPOSE 5000
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "example.com/moby/dockerfileexpansion:test"
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

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

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

	require.Equal(t, 3, len(ociimg.Config.ExposedPorts))

	var ports []string
	for p := range ociimg.Config.ExposedPorts {
		ports = append(ports, p)
	}
	slices.Sort(ports)

	require.Equal(t, "3000/tcp", ports[0])
	require.Equal(t, "4000/udp", ports[1])
	require.Equal(t, "5000/tcp", ports[2])
}

// moby/moby#10858
func testDockerfileLowercase(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
`, `
FROM nanoserver
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("dockerfile", dockerfile, 0600),
	)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(ctx, c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testLabels(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
LABEL foo=bar
`,
		`
FROM nanoserver
LABEL foo=bar
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "example.com/moby/dockerfilelabels:test"
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"label:bar": "baz",
		},
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

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

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

	v, ok := ociimg.Config.Labels["foo"]
	require.True(t, ok)
	require.Equal(t, "bar", v)

	v, ok = ociimg.Config.Labels["bar"]
	require.True(t, ok)
	require.Equal(t, "baz", v)
}

func testUser(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "USER instruction tests rely on Unix /etc/passwd, /etc/group, and id command which are not available on Windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS base
RUN mkdir -m 0777 /out
RUN id -un > /out/rootuser

# Make sure our defaults work
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)" = '0:0/root:root' ]

# TODO decide if "args.user = strconv.Itoa(syscall.Getuid())" is acceptable behavior for changeUser in sysvinit instead of "return nil" when "USER" isn't specified (so that we get the proper group list even if that is the empty list, even in the default case of not supplying an explicit USER to run as, which implies USER 0)
USER root
RUN [ "$(id -G):$(id -Gn)" = '0 10:root wheel' ]

# Setup dockerio user and group
RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd && \
	echo 'dockerio:x:1001:' >> /etc/group

# Make sure we can switch to our user and all the information is exactly as we expect it to be
USER dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]

# Switch back to root and double check that worked exactly as we might expect it to
USER root
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '0:0/root:root/0 10:root wheel' ] && \
        # Add a "supplementary" group for our dockerio user
	echo 'supplementary:x:1002:dockerio' >> /etc/group

# ... and then go verify that we get it like we expect
USER dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001 1002:dockerio supplementary' ]
USER 1001
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001 1002:dockerio supplementary' ]

# super test the new "user:group" syntax
USER dockerio:dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER 1001:dockerio
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER dockerio:1001
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER 1001:1001
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1001/dockerio:dockerio/1001:dockerio' ]
USER dockerio:supplementary
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]
USER dockerio:1002
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]
USER 1001:supplementary
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]
USER 1001:1002
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1001:1002/dockerio:supplementary/1002:supplementary' ]

# make sure unknown uid/gid still works properly
USER 1042:1043
RUN [ "$(id -u):$(id -g)/$(id -un):$(id -gn)/$(id -G):$(id -Gn)" = '1042:1043/1042:1043/1043:1043' ]
USER daemon
RUN id -un > /out/daemonuser
FROM scratch
COPY --from=base /out /
USER nobody
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

	dt, err := os.ReadFile(filepath.Join(destDir, "rootuser"))
	require.NoError(t, err)
	require.Equal(t, "root\n", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "daemonuser"))
	require.NoError(t, err)
	require.Equal(t, "daemon\n", string(dt))

	// test user in exported
	target := "example.com/moby/dockerfileuser:test"
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

	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err = content.ReadBlob(ctx, client.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.Equal(t, "nobody", ociimg.Config.User)
}

// testUserAdditionalGids ensures that the primary GID is also included in the additional GID list.
// CVE-2023-25173: https://github.com/advisories/GHSA-hmfx-3pcx-653p
func testUserAdditionalGids(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows", "Tests Unix GID behavior using id command and /etc/passwd, not applicable to Windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
# Mimics the tests in https://github.com/containerd/containerd/commit/3eda46af12b1deedab3d0802adb2e81cb3521950
FROM busybox
SHELL ["/bin/sh", "-euxc"]
RUN [ "$(id)" = "uid=0(root) gid=0(root) groups=0(root),10(wheel)" ]
USER 1234
RUN [ "$(id)" = "uid=1234 gid=0(root) groups=0(root)" ]
USER 1234:1234
RUN [ "$(id)" = "uid=1234 gid=1234 groups=1234" ]
USER daemon
RUN [ "$(id)" = "uid=1(daemon) gid=1(daemon) groups=1(daemon)" ]
`)

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

// moby/buildkit#1301
func testDockerfileCheckHostname(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox
RUN cat /etc/hosts | grep foo
RUN echo $HOSTNAME | grep foo
	RUN echo $(hostname) | grep foo
	`,
		`
FROM nanoserver
RUN  reg query "HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters" /v Hostname | findstr "foo"
	`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	cases := []struct {
		name  string
		attrs map[string]string
	}{
		{
			name: "meta",
			attrs: map[string]string{
				"hostname": "foo",
			},
		},
		{
			name: "arg",
			attrs: map[string]string{
				"build-arg:BUILDKIT_SANDBOX_HOSTNAME": "foo",
			},
		},
		{
			name: "meta and arg",
			attrs: map[string]string{
				"hostname":                            "bar",
				"build-arg:BUILDKIT_SANDBOX_HOSTNAME": "foo",
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err = f.Solve(sb.Context(), c, client.SolveOpt{
				FrontendAttrs: tt.attrs,
				LocalMounts: map[string]fsutil.FS{
					dockerui.DefaultLocalNameDockerfile: dir,
					dockerui.DefaultLocalNameContext:    dir,
				},
			}, nil)
			require.NoError(t, err)
		})
	}
}

func testStepNames(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM busybox AS base
WORKDIR /out
RUN echo "base" > base
FROM scratch
COPY --from=base --chmod=0644 /out /out
`,
		`
FROM nanoserver:latest AS base
WORKDIR /out
RUN echo base > base
FROM nanoserver:latest
COPY --from=base --chmod=0644 /out /out
`,
	))

	expectedRunStep := integration.UnixOrWindows(
		`[base 3/3] RUN echo "base" > base`,
		`[base 3/3] RUN echo base > base`,
	)

	// Step numbering differs between platforms due to base image characteristics:
	// - Unix uses 'scratch' (empty image) so COPY is step 1/1
	// - Windows uses 'nanoserver' (full image) which has internal setup steps, making COPY step 2/2
	expectedCopyStep := integration.UnixOrWindows(
		`[stage-1 1/1] COPY --from=base --chmod=0644 /out /out`,
		`[stage-1 2/2] COPY --from=base --chmod=0644 /out /out`,
	)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	f := getFrontend(t, sb)

	ch := make(chan *client.SolveStatus)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		_, err = f.Solve(ctx, c, client.SolveOpt{
			LocalMounts: map[string]fsutil.FS{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, ch)
		return err
	})

	eg.Go(func() error {
		hasCopy := false
		hasRun := false
		visited := make(map[string]struct{})
		for status := range ch {
			for _, vtx := range status.Vertexes {
				if _, ok := visited[vtx.Name]; ok {
					continue
				}
				visited[vtx.Name] = struct{}{}
				t.Logf("step: %q", vtx.Name)
				switch vtx.Name {
				case expectedRunStep:
					hasRun = true
				case expectedCopyStep:
					hasCopy = true
				}
			}
		}
		if !hasCopy {
			return errors.New("missing copy step")
		}
		if !hasRun {
			return errors.New("missing run step")
		}
		return nil
	})

	err = eg.Wait()
	require.NoError(t, err)
}

func testTargetMistype(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)

	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(`
FROM scratch AS build
COPY Dockerfile /out

FROM scratch
COPY --from=build /out /
`, `
FROM nanoserver:latest AS build
COPY Dockerfile C:\out

FROM nanoserver:latest
COPY --from=build C:\out C:\
`))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"target": "bulid",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "target stage \"bulid\" could not be found (did you mean build?)")
}
