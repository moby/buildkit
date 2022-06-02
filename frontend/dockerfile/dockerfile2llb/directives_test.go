package dockerfile2llb

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirectives(t *testing.T) {
	t.Parallel()

	// Test basic directive parsing
	dt := `#escape=\
# key = FOO bar

# something
`

	d := ParseDirectives(bytes.NewBuffer([]byte(dt)))
	require.Equal(t, len(d), 2, fmt.Sprintf("%+v", d))

	v, ok := d["escape"]
	require.True(t, ok)
	require.Equal(t, v.Value, "\\")

	v, ok = d["key"]
	require.True(t, ok)
	require.Equal(t, v.Value, "FOO bar")

	// Keys are treated as lower-case
	dt = `# EScape=\
# KEY = FOO bar

# something
`

	d = ParseDirectives(bytes.NewBuffer([]byte(dt)))
	require.Equal(t, len(d), 2, fmt.Sprintf("%+v", d))

	v, ok = d["escape"]
	require.True(t, ok)
	require.Equal(t, v.Value, "\\")

	v, ok = d["key"]
	require.True(t, ok)
	require.Equal(t, v.Value, "FOO bar")

	// Test with shebang line(s), even with an = symbol
	dt = `#!/usr/bin/env=1
#! /usr/bin/env 1
#KEY=FOO

# something
`

	d = ParseDirectives(bytes.NewBuffer([]byte(dt)))
	require.Equal(t, len(d), 1, fmt.Sprintf("%+v", d))

	v, ok = d["key"]
	require.True(t, ok)
	require.Equal(t, v.Value, "FOO")
}

func TestSyntaxDirective(t *testing.T) {
	t.Parallel()

	dt := `# syntax = dockerfile:experimental // opts
FROM busybox
`

	ref, cmdline, loc, ok := DetectSyntax(bytes.NewBuffer([]byte(dt)))
	require.True(t, ok)
	require.Equal(t, ref, "dockerfile:experimental")
	require.Equal(t, cmdline, "dockerfile:experimental // opts")
	require.Equal(t, 1, loc[0].Start.Line)
	require.Equal(t, 1, loc[0].End.Line)

	dt = `FROM busybox
RUN ls
`
	ref, cmdline, _, ok = DetectSyntax(bytes.NewBuffer([]byte(dt)))
	require.False(t, ok)
	require.Equal(t, ref, "")
	require.Equal(t, cmdline, "")
}
