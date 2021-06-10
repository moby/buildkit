// +build dfheredoc

package parser

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExtractsHeredoc(t *testing.T) {
	dockerfile := bytes.NewBufferString(`
FROM alpine:3.6

ENV NAME=me

RUN ls

USER <<INVALID
INVALID

RUN <<EMPTY
EMPTY

RUN 3<<EMPTY2
EMPTY2

RUN "<<NOHEREDOC"

RUN <<INDENT
	foo
	bar
INDENT

RUN <<-UNINDENT
	baz
	quux
UNINDENT

RUN <<-UNINDENT2
	baz
	quux
	UNINDENT2

RUN <<-EXPAND
	expand $NAME
EXPAND

RUN <<-'NOEXPAND'
	don't expand $NAME
NOEXPAND

RUN <<COPY
echo hello world
echo foo bar
COPY

RUN <<COMMENT
# internal comment
echo hello world
echo foo bar # trailing comment
COMMENT

RUN --mount=type=cache,target=/foo <<MOUNT
echo hello
MOUNT

COPY <<FILE1 <<FILE2 /dest
content 1
FILE1
content 2
FILE2

COPY <<X <<Y /dest
Y
X
X
Y
	`)

	tests := [][]Heredoc{
		nil, // ENV EXAMPLE=bla
		nil, // RUN ls
		nil, // USER <<INVALID
		nil, // INVALID
		{
			// RUN <<EMPTY
			{
				Name:    "EMPTY",
				Content: "",
				Expand:  true,
			},
		},
		{
			// RUN <<EMPTY2
			{
				Name:           "EMPTY2",
				Content:        "",
				Expand:         true,
				FileDescriptor: 3,
			},
		},
		nil, // RUN "<<NOHEREDOC"
		{
			// RUN <<INDENT
			{
				Name:    "INDENT",
				Content: "\tfoo\n\tbar\n",
				Expand:  true,
			},
		},
		{
			// RUN <<-UNINDENT
			{
				Name:    "UNINDENT",
				Content: "\tbaz\n\tquux\n",
				Expand:  true,
				Chomp:   true,
			},
		},
		{
			// RUN <<-UNINDENT2
			{
				Name:    "UNINDENT2",
				Content: "\tbaz\n\tquux\n",
				Expand:  true,
				Chomp:   true,
			},
		},
		{
			// RUN <<-EXPAND
			{
				Name:    "EXPAND",
				Content: "\texpand $NAME\n",
				Expand:  true,
				Chomp:   true,
			},
		},
		{
			// RUN <<-'NOEXPAND'
			{
				Name:    "NOEXPAND",
				Content: "\tdon't expand $NAME\n",
				Expand:  false,
				Chomp:   true,
			},
		},
		{
			// RUN <<COPY
			{
				Name:    "COPY",
				Content: "echo hello world\necho foo bar\n",
				Expand:  true,
			},
		},
		{
			// RUN <<COMMENT
			{
				Name:    "COMMENT",
				Content: "# internal comment\necho hello world\necho foo bar # trailing comment\n",
				Expand:  true,
			},
		},
		{
			// RUN <<MOUNT
			{
				Name:    "MOUNT",
				Content: "echo hello\n",
				Expand:  true,
			},
		},
		{
			// COPY <<FILE1 <<FILE2 /dest
			{
				Name:    "FILE1",
				Content: "content 1\n",
				Expand:  true,
			},
			{
				Name:    "FILE2",
				Content: "content 2\n",
				Expand:  true,
			},
		},
		{
			// COPY <<X <<Y /dest
			{
				Name:    "X",
				Content: "Y\n",
				Expand:  true,
			},
			{
				Name:    "Y",
				Content: "X\n",
				Expand:  true,
			},
		},
	}

	result, err := Parse(dockerfile)
	require.NoError(t, err)

	for i, test := range tests {
		child := result.AST.Children[i+1]
		require.Equal(t, test, child.Heredocs)
	}
}

func TestParseJSONHeredoc(t *testing.T) {
	dockerfile := bytes.NewBufferString(`
FROM alpine:3.6

RUN ["whoami"]
RUN ["<<EOF"]
RUN ["<<'EOF'"]
	`)

	result, err := Parse(dockerfile)
	require.NoError(t, err)

	for i := 1; i <= 3; i++ {
		child := result.AST.Children[i]
		require.Nil(t, child.Heredocs)
	}
}

func TestHeredocChomp(t *testing.T) {
	content := "\thello\n\tworld\n"
	require.Equal(t, "hello\nworld\n", ChompHeredocContent(content))
}

func TestParseHeredocHelpers(t *testing.T) {
	validHeredocs := []string{
		"<<EOF",
		"<<'EOF'",
		`<<"EOF"`,
		"<<-EOF",
		"<<-'EOF'",
		`<<-"EOF"`,
	}
	invalidHeredocs := []string{
		"<<'EOF",
		"<<\"EOF",
		"<<EOF'",
		"<<EOF\"",
	}
	notHeredocs := []string{
		"",
		"EOF",
		"<<",
		"<<-",
		"<EOF",
		"<<<EOF",
	}
	for _, src := range notHeredocs {
		heredoc, err := ParseHeredoc(src)
		require.NoError(t, err)
		require.Nil(t, heredoc)
	}
	for _, src := range validHeredocs {
		heredoc, err := ParseHeredoc(src)
		require.NoError(t, err)
		require.Equal(t, heredoc.Name, "EOF")
	}
	for _, src := range invalidHeredocs {
		_, err := ParseHeredoc(src)
		require.Error(t, err)
	}
}

func TestHeredocsFromLine(t *testing.T) {
	srcs := []struct {
		line         string
		heredocNames []string
	}{
		{
			line:         "RUN <<EOF",
			heredocNames: []string{"EOF"},
		},
		{
			line:         "RUN <<-EOF",
			heredocNames: []string{"EOF"},
		},
		{
			line:         "RUN <<'EOF'",
			heredocNames: []string{"EOF"},
		},
		{
			line:         "RUN 4<<EOF",
			heredocNames: []string{"EOF"},
		},
		{
			line:         "RUN <<EOF <<EOF2",
			heredocNames: []string{"EOF", "EOF2"},
		},
		{
			line:         "RUN '<<EOF'",
			heredocNames: nil,
		},
		{
			line:         `RUN "<<EOF"`,
			heredocNames: nil,
		},
	}

	for _, src := range srcs {
		heredocs, err := heredocsFromLine(src.line)
		require.NoError(t, err)
		for i, heredoc := range heredocs {
			require.Equal(t, heredoc.Name, src.heredocNames[i])
		}
	}
}
