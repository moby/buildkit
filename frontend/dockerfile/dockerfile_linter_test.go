package dockerfile

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests/lint"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

var lintTests = integration.TestFuncs(
	testLintRules,
)

func testLintRules(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureFrontendOutline)
	f := getFrontend(t, sb)
	if _, ok := f.(*clientFrontend); !ok {
		t.Skip("only test with client frontend")
	}

	dockerfile := []byte(`
# 'First' should produce a lint warning (lowercase stage name)
FROM scratch as First

# 'Run' should produce a lint warning (inconsistent command case)
Run echo "Hello"

# No lint warning
run echo "World"

# No lint warning
RUN touch /file

# No lint warning
FROM scratch as second

# 'First' should produce a lint warning (lowercase flags)
COPY --from=First /file /file

# No lint warning
FROM scratch as target

# No lint warning
COPY --from=second /file /file
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", []byte(dockerfile), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := os.MkdirTemp("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	called := false
	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		res, err := c.Solve(ctx, gateway.SolveRequest{
			FrontendOpt: map[string]string{
				"frontend.caps": "moby.buildkit.frontend.subrequests",
				"requestid":     "frontend.lint",
				"build-arg:BAR": "678",
				"target":        "target",
			},
			Frontend: "dockerfile.v0",
		})
		require.NoError(t, err)

		warnings, err := unmarshalLintWarnings(res)
		require.NoError(t, err)
		require.Len(t, warnings.Warnings, 3)

		warningList := warnings.Warnings
		sort.Slice(warningList, func(i, j int) bool {
			return warningList[i].Location.Start.Line < warningList[j].Location.Start.Line
		})

		require.Equal(t, 3, warningList[0].Location.Start.Line)
		require.Equal(t, linter.LinterRules[linter.RuleStageNameCasing].Short, warningList[0].Short)

		require.Equal(t, 6, warningList[1].Location.Start.Line)
		require.Equal(t, linter.LinterRules[linter.RuleCommandCasing].Short, warningList[1].Short)

		require.Equal(t, 18, warningList[2].Location.Start.Line)
		require.Equal(t, linter.LinterRules[linter.RuleFlagCasing].Short, warningList[2].Short)

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

func unmarshalLintWarnings(res *gateway.Result) (*lint.WarningList, error) {
	dt, ok := res.Metadata["result.json"]
	if !ok {
		return nil, errors.Errorf("missing frontend.lint")
	}
	var warnings lint.WarningList
	if err := json.Unmarshal(dt, &warnings); err != nil {
		return nil, err
	}
	return &warnings, nil
}
