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
	testLintRequest,
	testLintStageName,
	testLintNoEmptyContinuations,
)

func testLintRequest(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureFrontendOutline)
	f := getFrontend(t, sb)
	if _, ok := f.(*clientFrontend); !ok {
		t.Skip("only test with client frontend")
	}

	dockerfile := []byte(`
	FROM scratch as target
	COPY Dockerfile \

	.
	ARG inherited=box
	copy Dockerfile /foo

	FROM scratch AS target2
	COPY Dockerfile /Dockerfile
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

		lintResults, err := unmarshalLintResults(res)
		require.NoError(t, err)

		require.Equal(t, 3, len(lintResults.Warnings))
		sort.Slice(lintResults.Warnings, func(i, j int) bool {
			// sort by line number in ascending order
			return lintResults.Warnings[i].StartLine < lintResults.Warnings[j].StartLine
		})
		checkLintRequestWarnings(t, lintResults.Warnings, []lint.Warning{
			{
				RuleName:  "FromAsCasing",
				Detail:    "'as' and 'FROM' keywords' casing do not match (line 2)",
				Filename:  "Dockerfile",
				StartLine: 2,
				Source:    []string{"\tFROM scratch as target"},
			},
			{
				RuleName:  "NoEmptyContinuations",
				Detail:    "Empty continuation line (line 5)",
				Filename:  "Dockerfile",
				StartLine: 5,
				Source:    []string{"\t."},
			},
			{
				RuleName:  "FileConsistentCommandCasing",
				Detail:    "Command 'copy' should match the case of the command majority (uppercase) (line 7)",
				Filename:  "Dockerfile",
				StartLine: 7,
				Source:    []string{"\tcopy Dockerfile /foo"},
			},
		})
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

func testLintStageName(t *testing.T, sb integration.Sandbox) {
	dockerfile := []byte(`
# warning: stage name should be lowercase
FROM scratch AS BadStageName

# warning: 'as' should match 'FROM' cmd casing.
FROM scratch as base2

FROM scratch AS base3
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			Short:  "Lint Rule 'StageNameCasing': Stage name 'BadStageName' should be lowercase (line 3)",
			Detail: "Stage names should be lowercase",
			Level:  1,
		},
		{
			Short:  "Lint Rule 'FromAsCasing': 'as' and 'FROM' keywords' casing do not match (line 6)",
			Detail: "The 'as' keyword should match the case of the 'from' keyword",
			Level:  1,
		},
	})

	dockerfile = []byte(`
# warning: 'AS' should match 'from' cmd casing.
from scratch AS base

from scratch as base2
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			Short:  "Lint Rule 'FromAsCasing': 'AS' and 'from' keywords' casing do not match (line 3)",
			Detail: "The 'as' keyword should match the case of the 'from' keyword",
			Level:  1,
		},
	})
}

func testLintNoEmptyContinuations(t *testing.T, sb integration.Sandbox) {
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
			Short:  "Lint Rule 'NoEmptyContinuations': Empty continuation line (line 6)",
			Detail: "Empty continuation lines will become errors in a future release",
			URL:    "https://github.com/moby/moby/pull/33719",
			Level:  1,
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
			Short:  "Lint Rule 'SelfConsistentCommandCasing': Command 'From' should be consistently cased (line 3)",
			Detail: "Commands should be in consistent casing (all lower or all upper)",
			Level:  1,
		},
	})
	dockerfile = []byte(`
# warning: 'FROM' should be either lowercased or uppercased
frOM scratch as base
from scratch as base2
`)
	checkLinterWarnings(t, sb, dockerfile, []expectedLintWarning{
		{
			Short:  "Lint Rule 'SelfConsistentCommandCasing': Command 'frOM' should be consistently cased (line 3)",
			Detail: "Commands should be in consistent casing (all lower or all upper)",
			Level:  1,
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
			Short:  "Lint Rule 'FileConsistentCommandCasing': Command 'copy' should match the case of the command majority (uppercase) (line 4)",
			Detail: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Level:  1,
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
			Short:  "Lint Rule 'FileConsistentCommandCasing': Command 'COPY' should match the case of the command majority (lowercase) (line 4)",
			Detail: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Level:  1,
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
			Short:  "Lint Rule 'FileConsistentCommandCasing': Command 'from' should match the case of the command majority (uppercase) (line 3)",
			Detail: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Level:  1,
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
			Short:  "Lint Rule 'FileConsistentCommandCasing': Command 'FROM' should match the case of the command majority (lowercase) (line 3)",
			Detail: "All commands within the Dockerfile should use the same casing (either upper or lower)",
			Level:  1,
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

func checkLinterWarnings(t *testing.T, sb integration.Sandbox, dockerfile []byte, expected []expectedLintWarning) {
	// As a note, this test depends on the `expected` lint
	// warnings to be in order of appearance in the Dockerfile.

	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

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

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
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
		require.Equal(t, expected[i].Short, string(w.Short))
		require.Equal(t, expected[i].Detail, string(w.Detail[0]))
		require.Equal(t, expected[i].URL, w.URL)
		require.Equal(t, expected[i].Level, w.Level)
	}
}

func checkLintRequestWarnings(t *testing.T, actual, expected []lint.Warning) {
	require.Equal(t, len(expected), len(actual))

	for i, expected := range expected {
		actual := actual[i]
		require.Equal(t, expected.RuleName, actual.RuleName)
		require.Equal(t, expected.Detail, actual.Detail)
		require.Equal(t, expected.Filename, actual.Filename)
		require.Equal(t, expected.StartLine, actual.StartLine)
		require.Equal(t, len(expected.Source), len(actual.Source))
		for j, expectedSource := range expected.Source {
			require.Equal(t, expectedSource, actual.Source[j])
		}
	}
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
