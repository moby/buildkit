package dockerfile

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

var addUseragentTests = integration.TestFuncs(
	testAddUseragent,
)

func init() {
	allTests = append(allTests, addUseragentTests...)
}

func testAddUseragent(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	var capturedUserAgent string

	handler := func(w http.ResponseWriter, r *http.Request) {
		capturedUserAgent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("content"))
	}

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	t.Run("Custom User-Agent", func(t *testing.T) {
		dockerfile := []byte(fmt.Sprintf(`
FROM scratch
ADD --user-agent="%s" %s /tmp/foo
`, "Mozilla/5.0 (Linux) Foo 5.0", server.URL))
		dir := integration.Tmpdir(
			t,
			fstest.CreateFile("Dockerfile", dockerfile, 0600),
		)
		_, err := f.Solve(sb.Context(), c, client.SolveOpt{
			LocalDirs: map[string]string{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.NoError(t, err)
		require.Equal(t, "Mozilla/5.0 (Linux) Foo 5.0", capturedUserAgent)
	})
	t.Run("NonHTTPSource", func(t *testing.T) {
		foo := []byte("local file")
		dockerfile := []byte(fmt.Sprintf(`
FROM scratch
ADD --user-agent=%s foo /tmp/foo
`, "Foo"))
		dir := integration.Tmpdir(
			t,
			fstest.CreateFile("foo", foo, 0600),
			fstest.CreateFile("Dockerfile", dockerfile, 0600),
		)
		_, err := f.Solve(sb.Context(), c, client.SolveOpt{
			LocalDirs: map[string]string{
				dockerui.DefaultLocalNameDockerfile: dir,
				dockerui.DefaultLocalNameContext:    dir,
			},
		}, nil)
		require.Error(t, err, "user-agent can't be specified for non-HTTP sources")
	})
}
