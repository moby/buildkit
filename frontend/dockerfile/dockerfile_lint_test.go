package dockerfile

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"

	"github.com/moby/buildkit/frontend/subrequests/lint"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

var lintTests = integration.TestFuncs(
	testStageName,
	testNoEmptyContinuations,
	testSelfConsistentCommandCasing,
	testFileConsistentCommandCasing,
)

func testStageName(t *testing.T, sb integration.Sandbox) {
	dockerfile := []byte(`
# warning: stage name should be lowercase
FROM scratch AS BadStageName

# warning: 'as' should match 'FROM' cmd casing.
FROM scratch as base2

FROM scratch AS base3
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "StageNameCasing",
			Description: "Stage names should be lowercase",
			Detail:      "Stage name 'BadStageName' should be lowercase",
			Line:        3,
			Level:       1,
		},
		{
			RuleName:    "FromAsCasing",
			Description: "The 'as' keyword should match the case of the 'from' keyword",
			Detail:      "'as' and 'FROM' keywords' casing do not match",
			Line:        6,
			Level:       1,
		},
	})

	dockerfile = []byte(`
# warning: 'AS' should match 'from' cmd casing.
from scratch AS base

from scratch as base2
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "FromAsCasing",
			Description: "The 'as' keyword should match the case of the 'from' keyword",
			Detail:      "'AS' and 'from' keywords' casing do not match",
			Level:       1,
			Line:        3,
		},
	})
}

func testNoEmptyContinuations(t *testing.T, sb integration.Sandbox) {
	dockerfile := []byte(`
FROM scratch
# warning: empty continuation line
COPY Dockerfile \

.
COPY Dockerfile \
.
`)

	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "NoEmptyContinuations",
			Description: "Empty continuation lines will become errors in a future release",
			Detail:      "Empty continuation line",
			Level:       1,
			Line:        6,
			URL:         "https://github.com/moby/moby/pull/33719",
		},
	})
}

func testSelfConsistentCommandCasing(t *testing.T, sb integration.Sandbox) {
	dockerfile := []byte(`
# warning: 'FROM' should be either lowercased or uppercased
From scratch as base
FROM scratch AS base2
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "SelfConsistentCommandCasing",
			Description: "Commands should be in consistent casing (all lower or all upper)",
			Detail:      "Command 'From' should be consistently cased",
			Level:       1,
			Line:        3,
		},
	})
	dockerfile = []byte(`
# warning: 'FROM' should be either lowercased or uppercased
frOM scratch as base
from scratch as base2
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "SelfConsistentCommandCasing",
			Description: "Commands should be in consistent casing (all lower or all upper)",
			Detail:      "Command 'frOM' should be consistently cased",
			Line:        3,
			Level:       1,
		},
	})
}

func testFileConsistentCommandCasing(t *testing.T, sb integration.Sandbox) {
	dockerfile := []byte(`
FROM scratch
# warning: 'copy' should match command majority's casing (uppercase)
copy Dockerfile /foo
COPY Dockerfile /bar
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "FileConsistentCommandCasing",
			Description: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Detail:      "Command 'copy' should match the case of the command majority (uppercase)",
			Line:        4,
			Level:       1,
		},
	})

	dockerfile = []byte(`
from scratch
# warning: 'COPY' should match command majority's casing (lowercase)
COPY Dockerfile /foo
copy Dockerfile /bar
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "FileConsistentCommandCasing",
			Description: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Detail:      "Command 'COPY' should match the case of the command majority (lowercase)",
			Line:        4,
			Level:       1,
		},
	})

	dockerfile = []byte(`
# warning: 'from' should match command majority's casing (uppercase)
from scratch
COPY Dockerfile /foo
COPY Dockerfile /bar
COPY Dockerfile /baz
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "FileConsistentCommandCasing",
			Description: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Detail:      "Command 'from' should match the case of the command majority (uppercase)",
			Line:        3,
			Level:       1,
		},
	})

	dockerfile = []byte(`
# warning: 'FROM' should match command majority's casing (lowercase)
FROM scratch
copy Dockerfile /foo
copy Dockerfile /bar
copy Dockerfile /baz
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			RuleName:    "FileConsistentCommandCasing",
			Description: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Detail:      "Command 'FROM' should match the case of the command majority (lowercase)",
			Line:        3,
			Level:       1,
		},
	})

	dockerfile = []byte(`
from scratch
copy Dockerfile /foo
copy Dockerfile /bar
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{})

	dockerfile = []byte(`
FROM scratch
COPY Dockerfile /foo
COPY Dockerfile /bar
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{})
}

func checkUnmarshal(t *testing.T, sb integration.Sandbox, c *client.Client, dir *integration.TmpDirWithName, expected []expectedLintWarning) {
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
			},
			Frontend: "dockerfile.v0",
		})
		require.NoError(t, err)

		lintResults, err := unmarshalLintResults(res)
		require.NoError(t, err)

		require.Equal(t, len(expected), len(lintResults.Warnings))
		sort.Slice(lintResults.Warnings, func(i, j int) bool {
			// sort by line number in ascending order
			firstRange := lintResults.Warnings[i].Location.Ranges[0]
			secondRange := lintResults.Warnings[j].Location.Ranges[0]
			return firstRange.Start.Line < secondRange.Start.Line
		})
		// Compare expectedLintWarning with actual lint results
		for i, w := range lintResults.Warnings {
			checkLintWarning(t, w, expected[i])
		}
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

func checkProgressStream(t *testing.T, sb integration.Sandbox, c *client.Client, dir *integration.TmpDirWithName, expected []expectedLintWarning) {
	status := make(chan *client.SolveStatus)
	statusDone := make(chan struct{})
	done := make(chan struct{})

	var warnings []*client.VertexWarning

	go func() {
		defer close(statusDone)
		for {
			select {
			case st, ok := <-status:
				if !ok {
					return
				}
				warnings = append(warnings, st.Warnings...)
			case <-done:
				return
			}
		}
	}()

	f := getFrontend(t, sb)

	_, err := f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"platform": "linux/amd64,linux/arm64",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, status)
	require.NoError(t, err)

	select {
	case <-statusDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for statusDone")
	}

	// two platforms only show one warning
	require.Equal(t, len(expected), len(warnings))
	sort.Slice(warnings, func(i, j int) bool {
		w1 := warnings[i]
		w2 := warnings[j]
		if len(w1.Range) == 0 {
			return true
		} else if len(w2.Range) == 0 {
			return false
		}
		return w1.Range[0].Start.Line < w2.Range[0].Start.Line
	})
	for i, w := range warnings {
		checkVertexWarning(t, w, expected[i])
	}
}

func checkLinterWarnings(t *testing.T, sb integration.Sandbox, dockerfile []byte, expected []expectedLintWarning) {
	// As a note, this test depends on the `expected` lint
	// warnings to be in order of appearance in the Dockerfile.

	integration.SkipOnPlatform(t, "windows")

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	t.Run("warntype=progress", func(t *testing.T) {
		checkProgressStream(t, sb, c, dir, expected)
	})
	t.Run("warntype=unmarshal", func(t *testing.T) {
		checkUnmarshal(t, sb, c, dir, expected)
	})
}

func checkVertexWarning(t *testing.T, warning *client.VertexWarning, expected expectedLintWarning) {
	short := linter.LintFormatShort(expected.RuleName, expected.Detail, expected.Line)
	require.Equal(t, short, string(warning.Short))
	require.Equal(t, expected.Description, string(warning.Detail[0]))
	require.Equal(t, expected.URL, warning.URL)
	require.Equal(t, expected.Level, warning.Level)
}

func checkLintWarning(t *testing.T, warning lint.Warning, expected expectedLintWarning) {
	require.Equal(t, expected.RuleName, warning.RuleName)
	require.Equal(t, expected.Description, warning.Description)
	require.Equal(t, expected.URL, warning.URL)
	require.Equal(t, expected.Detail, warning.Detail)
}

func unmarshalLintResults(res *gateway.Result) (*lint.LintResults, error) {
	dt, ok := res.Metadata["result.json"]
	if !ok {
		return nil, errors.Errorf("missing frontend.outline")
	}
	var l lint.LintResults
	if err := json.Unmarshal(dt, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

type expectedLintWarning struct {
	RuleName    string
	Description string
	Line        int
	Detail      string
	URL         string
	Level       int
}
