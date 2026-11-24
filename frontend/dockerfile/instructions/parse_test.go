package instructions

import (
	"bytes"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/command"
	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/stretchr/testify/require"
)

func TestCommandsExactlyOneArgument(t *testing.T) {
	commands := []string{
		"MAINTAINER",
		"WORKDIR",
		"USER",
		"STOPSIGNAL",
	}

	for _, cmd := range commands {
		ast, err := parser.Parse(strings.NewReader(cmd))
		require.NoError(t, err)
		_, err = ParseInstruction(ast.AST.Children[0])
		require.EqualError(t, err, errExactlyOneArgument(cmd).Error())
	}
}

func TestCommandsAtLeastOneArgument(t *testing.T) {
	commands := []string{
		"ENV",
		"LABEL",
		"ONBUILD",
		"HEALTHCHECK",
		"EXPOSE",
		"VOLUME",
	}

	for _, cmd := range commands {
		ast, err := parser.Parse(strings.NewReader(cmd))
		require.NoError(t, err)
		_, err = ParseInstruction(ast.AST.Children[0])
		require.EqualError(t, err, errAtLeastOneArgument(cmd).Error())
	}
}

func TestCommandsNoDestinationArgument(t *testing.T) {
	commands := []string{
		"ADD",
		"COPY",
	}

	for _, cmd := range commands {
		ast, err := parser.Parse(strings.NewReader(cmd + " arg1"))
		require.NoError(t, err)
		_, err = ParseInstruction(ast.AST.Children[0])
		require.EqualError(t, err, errNoDestinationArgument(cmd).Error())
	}
}

func TestCommandsTooManyArguments(t *testing.T) {
	commands := []string{
		"ENV",
		"LABEL",
	}

	for _, cmd := range commands {
		node := &parser.Node{
			Original: cmd + "arg1 arg2 arg3",
			Value:    strings.ToLower(cmd),
			Next: &parser.Node{
				Value: "arg1",
				Next: &parser.Node{
					Value: "arg2",
					Next: &parser.Node{
						Value: "arg3",
						Next: &parser.Node{
							Value: "",
						},
					},
				},
			},
		}
		_, err := ParseInstruction(node)
		require.EqualError(t, err, errTooManyArguments(cmd).Error())
	}
}

func TestCommandsBlankNames(t *testing.T) {
	commands := []string{
		"ENV",
		"LABEL",
	}

	for _, cmd := range commands {
		node := &parser.Node{
			Original: cmd + " =arg2",
			Value:    strings.ToLower(cmd),
			Next: &parser.Node{
				Value: "",
				Next: &parser.Node{
					Value: "arg2",
					Next: &parser.Node{
						Value: "=",
					},
				},
			},
		}
		_, err := ParseInstruction(node)
		require.EqualError(t, err, errBlankCommandNames(cmd).Error())
	}
}

func TestHealthCheckCmd(t *testing.T) {
	node := &parser.Node{
		Value: command.Healthcheck,
		Next: &parser.Node{
			Value: "CMD",
			Next: &parser.Node{
				Value: "hello",
				Next: &parser.Node{
					Value: "world",
				},
			},
		},
	}
	cmd, err := ParseInstruction(node)
	require.NoError(t, err)
	hc, ok := cmd.(*HealthCheckCommand)
	require.Equal(t, true, ok)
	expected := []string{"CMD-SHELL", "hello world"}
	require.Equal(t, expected, hc.Health.Test)
}

func TestParseOptInterval(t *testing.T) {
	flInterval := &Flag{
		name:     "interval",
		flagType: stringType,
		Value:    "50ns",
	}
	_, err := parseOptInterval(flInterval)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be less than 1ms")

	flInterval.Value = "0ms"
	_, err = parseOptInterval(flInterval)
	require.NoError(t, err)

	flInterval.Value = "1ms"
	_, err = parseOptInterval(flInterval)
	require.NoError(t, err)
}

func TestNilLinter(t *testing.T) {
	for cmd := range command.Commands {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()

			for _, tc := range []string{
				cmd + " foo=bar",
				cmd + " a",
				cmd + " a b",
				cmd + " a b c",
				cmd + " 0 0",
			} {
				t.Run(tc, func(t *testing.T) {
					ast, err := parser.Parse(strings.NewReader("FROM busybox\n" + tc))
					if err == nil {
						_, _, _ = Parse(ast.AST, nil)
					}
				})
			}
		})
	}
}

func TestCommentsDetection(t *testing.T) {
	dt := `# foo sets foo
#
# renovate: datasource=docker depName=golang
ARG foo=bar

# base defines first stage
#
# Additional details about the base stage.
#
FROM busybox AS base
# this is irrelevant
ARG foo
# bar defines bar
#
# Additional details about bar.
#
# baz is something else
#
ARG bar baz=123
`

	ast, err := parser.Parse(bytes.NewBuffer([]byte(dt)))
	require.NoError(t, err)

	stages, meta, err := Parse(ast.AST, nil)
	require.NoError(t, err)

	require.Equal(t, "defines first stage", stages[0].DocComment)
	require.Equal(t, "foo", meta[0].Args[0].Key)
	require.Equal(t, "sets foo", meta[0].Args[0].DocComment)

	st := stages[0]

	require.Equal(t, "foo", st.Commands[0].(*ArgCommand).Args[0].Key)
	require.Equal(t, "", st.Commands[0].(*ArgCommand).Args[0].DocComment)
	require.Equal(t, "bar", st.Commands[1].(*ArgCommand).Args[0].Key)
	require.Equal(t, "defines bar", st.Commands[1].(*ArgCommand).Args[0].DocComment)
	require.Equal(t, "baz", st.Commands[1].(*ArgCommand).Args[1].Key)
	require.Equal(t, "is something else", st.Commands[1].(*ArgCommand).Args[1].DocComment)
}

func TestErrorCases(t *testing.T) {
	cases := []struct {
		name          string
		dockerfile    string
		expectedError string
	}{
		{
			name: "copyEmptyWhitespace",
			dockerfile: `COPY	
		quux \
      bar`,
			expectedError: "COPY requires at least two arguments",
		},
		{
			name:          "ONBUILD forbidden FROM",
			dockerfile:    "ONBUILD FROM scratch",
			expectedError: "FROM isn't allowed as an ONBUILD trigger",
		},
		{
			name:          "MAINTAINER unknown flag",
			dockerfile:    "MAINTAINER --boo joe@example.com",
			expectedError: "unknown flag: --boo",
		},
		{
			name:          "ARG unknown flag",
			dockerfile:    "ARG --boo foo",
			expectedError: "unknown flag: --boo",
		},
		{
			name:          "STOPSIGNAL unknown flag",
			dockerfile:    "STOPSIGNAL --boo SIGTERM",
			expectedError: "unknown flag: --boo",
		},
		{
			name:          "Chaining ONBUILD",
			dockerfile:    `ONBUILD ONBUILD RUN touch foobar`,
			expectedError: "Chaining ONBUILD via `ONBUILD ONBUILD` isn't allowed",
		},
		{
			name:          "Invalid instruction",
			dockerfile:    `FOO bar`,
			expectedError: "unknown instruction: FOO",
		},
		{
			name:          "Invalid instruction",
			dockerfile:    `foo bar`,
			expectedError: "unknown instruction: foo",
		},
	}
	for _, c := range cases {
		r := strings.NewReader(c.dockerfile)
		ast, err := parser.Parse(r)

		if err != nil {
			t.Fatalf("Error when parsing Dockerfile: %s", err)
		}
		n := ast.AST.Children[0]
		_, err = ParseInstruction(n)
		require.ErrorContains(t, err, c.expectedError)
	}
}

func TestUnsetDisabled(t *testing.T) {
	if unsetEnabled {
		t.Skip("--unset is enabled in this build")
	}
	for _, dockerfile := range []string{
		"ENV --unset FOO",
		"LABEL --unset foo",
		"ARG --unset FOO",
		"VOLUME --unset /foo",
	} {
		ast, err := parser.Parse(strings.NewReader(dockerfile))
		require.NoError(t, err)
		_, err = ParseInstruction(ast.AST.Children[0])
		require.ErrorContains(t, err, "unknown flag: --unset", dockerfile)
	}
}

func TestResetDisabled(t *testing.T) {
	if resetEnabled {
		t.Skip("--reset is enabled in this build")
	}
	ast, err := parser.Parse(strings.NewReader("FROM --reset=all busybox"))
	require.NoError(t, err)
	_, err = ParseInstruction(ast.AST.Children[0])
	require.ErrorContains(t, err, "unknown flag: --reset")
}

func TestRunCmdFlagsUsed(t *testing.T) {
	dockerfile := "RUN --mount=type=tmpfs,target=/foo/ echo hello"
	r := strings.NewReader(dockerfile)
	ast, err := parser.Parse(r)
	require.NoError(t, err)

	n := ast.AST.Children[0]
	c, err := ParseInstruction(n)
	require.NoError(t, err)
	require.IsType(t, &RunCommand{}, c)
	require.Equal(t, []string{"mount"}, c.(*RunCommand).FlagsUsed)
}

func BenchmarkParseBuildStageName(b *testing.B) {
	b.ReportAllocs()
	stageNames := []string{"STAGE_NAME", "StageName", "St4g3N4m3"}
	for b.Loop() {
		for _, s := range stageNames {
			_, _ = parseBuildStageName([]string{"foo", "as", s})
		}
	}
}

func TestCommentParagraphLint(t *testing.T) {
	for _, tc := range []struct {
		name        string
		comments    string
		warn        bool
		description string
	}{
		{
			name:        "paragraphs",
			comments:    "# foo describes foo\n#\n# renovate: datasource=docker\n#\n",
			description: "describes foo",
		},
		{
			name:     "non-description",
			comments: "# unrelated comment\n#\n",
			warn:     true,
		},
		{
			name:     "blank separator",
			comments: "# unrelated comment\n\n",
		},
		{
			name:     "whitespace separator",
			comments: "# foo describes foo\n \t\n",
		},
		{
			name:     "detached description",
			comments: "# foo describes foo\n\n",
		},
		{
			name:        "new comment block",
			comments:    "# foo old description\n\n# foo new description\n#\n",
			description: "new description",
		},
		{
			name:     "bare name",
			comments: "# foo\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, instruction := range []string{"ARG foo=1", "FROM scratch AS foo"} {
				ast, err := parser.Parse(strings.NewReader(tc.comments + instruction))
				require.NoError(t, err)
				var warnings []string
				lint := linter.New(&linter.Config{
					ExperimentalRules: []string{"InvalidDefinitionDescription"},
					Warn: func(name, _, _, _ string, _ []parser.Range) {
						warnings = append(warnings, name)
					},
				})
				stages, args, err := Parse(ast.AST, lint)
				require.NoError(t, err)
				if tc.warn {
					require.Equal(t, []string{"InvalidDefinitionDescription"}, warnings)
				} else {
					require.Empty(t, warnings)
				}
				if len(stages) > 0 {
					require.Equal(t, tc.description, stages[0].DocComment)
				} else {
					require.Equal(t, tc.description, args[0].Args[0].DocComment)
				}
			}
		})
	}
}

func TestCommentParagraphCheckScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		separator string
		warnings  int
	}{
		{
			name:      "paragraph",
			separator: "#\n",
			warnings:  1,
		},
		{
			name:      "blank line",
			separator: "\n",
			warnings:  2,
		},
		{
			name:      "whitespace line",
			separator: " \t\n",
			warnings:  2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The preceding instruction prevents this from being a file directive.
			ast, err := parser.Parse(strings.NewReader("FROM scratch\n# check=skip=MaintainerDeprecated\n" + tc.separator + "MAINTAINER example\nMAINTAINER next"))
			require.NoError(t, err)
			var warnings []string
			lint := linter.New(&linter.Config{
				Warn: func(name, _, _, _ string, _ []parser.Range) {
					warnings = append(warnings, name)
				},
			})
			_, _, err = Parse(ast.AST, lint)
			require.NoError(t, err)
			require.Len(t, warnings, tc.warnings)
			for _, warning := range warnings {
				require.Equal(t, "MaintainerDeprecated", warning)
			}
		})
	}
}
