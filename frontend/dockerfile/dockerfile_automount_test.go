package dockerfile

import (
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

var automountTests = integration.TestFuncs(
	testAutomountSecret,
	testAutomountSecretMultipleRuns,
	testAutomountMultiple,
	testAutomountCache,
	testAutomountBind,
	testAutomountTmpfs,
	testAutomountWithExplicitMount,
	testAutomountAppliesAllStages,
	testAutomountInvalidFrom,
	testAutomountInvalidSpec,
)

func init() {
	allTests = append(allTests, automountTests...)
}

func testAutomountSecret(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN [ "$(cat /run/secrets/mysecret)" = "secret-content" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=secret,id=mysecret,target=/run/secrets/mysecret",
		},
		Session: []session.Attachable{
			secretsprovider.FromMap(map[string][]byte{
				"mysecret": []byte("secret-content"),
			}),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountSecretMultipleRuns(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN [ "$(cat /run/secrets/token)" = "my-token" ]
RUN echo "Second RUN" && [ -f /run/secrets/token ]
RUN cat /run/secrets/token > /tmp/verify && [ "$(cat /tmp/verify)" = "my-token" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=secret,id=token,target=/run/secrets/token",
		},
		Session: []session.Attachable{
			secretsprovider.FromMap(map[string][]byte{
				"token": []byte("my-token"),
			}),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountMultiple(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN [ "$(cat /secrets/secret1)" = "value1" ]
RUN [ "$(cat /secrets/secret2)" = "value2" ]
RUN echo "cached" > /cache/test.txt
RUN [ "$(cat /cache/test.txt)" = "cached" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=secret,id=secret1,target=/secrets/secret1",
			"automount:1": "type=secret,id=secret2,target=/secrets/secret2",
			"automount:2": "type=cache,target=/cache",
		},
		Session: []session.Attachable{
			secretsprovider.FromMap(map[string][]byte{
				"secret1": []byte("value1"),
				"secret2": []byte("value2"),
			}),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountCache(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN echo "first" > /mycache/data.txt
RUN [ "$(cat /mycache/data.txt)" = "first" ]
RUN echo "second" >> /mycache/data.txt
RUN grep -q "first" /mycache/data.txt && grep -q "second" /mycache/data.txt
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=cache,target=/mycache,id=testcache",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountBind(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN [ "$(cat /context/config)" = "config-data" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("config", []byte("config-data"), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=bind,source=.,target=/context",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountTmpfs(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN touch /tmpdata/foo && [ -f /tmpdata/foo ]
RUN [ ! -f /tmpdata/foo ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=tmpfs,target=/tmpdata",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountWithExplicitMount(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN [ "$(cat /run/secrets/auto)" = "auto-secret" ]
RUN --mount=type=secret,id=explicit,target=/run/secrets/explicit [ "$(cat /run/secrets/explicit)" = "explicit-secret" ] && [ "$(cat /run/secrets/auto)" = "auto-secret" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=secret,id=auto,target=/run/secrets/auto",
		},
		Session: []session.Attachable{
			secretsprovider.FromMap(map[string][]byte{
				"auto":     []byte("auto-secret"),
				"explicit": []byte("explicit-secret"),
			}),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountAppliesAllStages(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox AS stage1
RUN [ "$(cat /run/secrets/shared)" = "shared-value" ]

FROM busybox AS stage2
RUN [ "$(cat /run/secrets/shared)" = "shared-value" ]

FROM busybox
RUN [ "$(cat /run/secrets/shared)" = "shared-value" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=secret,id=shared,target=/run/secrets/shared",
		},
		Session: []session.Attachable{
			secretsprovider.FromMap(map[string][]byte{
				"shared": []byte("shared-value"),
			}),
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAutomountInvalidFrom(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN echo "test"
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "type=bind,from=builder,target=/data",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "automount does not support 'from' option")
}

func testAutomountInvalidSpec(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN echo "test"
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"automount:0": "invalid-mount-spec",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to parse automount")
}
