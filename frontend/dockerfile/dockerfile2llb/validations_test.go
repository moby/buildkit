package dockerfile2llb

import (
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/stretchr/testify/require"
)

func TestSecretsUsedInArgOrEnvPSW(t *testing.T) {
	t.Parallel()

	df := `FROM scratch
ARG SOME_PSW=secret
ARG some_psw=secret
ENV OTHER_PSW=secret other_psw=secret
ARG SOME_USR=user
ENV SOME_PSWORD=value PUBLIC_PSW=public
`
	var warnings []string
	_, err := Dockerfile2LLB(t.Context(), []byte(df), ConvertOpt{
		Warn: func(ruleName, _, _, detail string, _ []parser.Range) {
			if ruleName == linter.RuleSecretsUsedInArgOrEnv.Name {
				warnings = append(warnings, detail)
			}
		},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		`Do not use ARG or ENV instructions for sensitive data (ARG "SOME_PSW")`,
		`Do not use ARG or ENV instructions for sensitive data (ARG "some_psw")`,
		`Do not use ARG or ENV instructions for sensitive data (ENV "OTHER_PSW")`,
		`Do not use ARG or ENV instructions for sensitive data (ENV "other_psw")`,
	}, warnings)
}
