package parser

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testDir = "testfiles"
const negativeTestDir = "testfiles-negative"
const testFileLineInfo = "testfile-line/Dockerfile"

func getDirs(t *testing.T, dir string) []string {
	f, err := os.Open(dir)
	require.NoError(t, err)
	defer f.Close()

	dirs, err := f.Readdirnames(0)
	require.NoError(t, err)
	return dirs
}

func TestParseErrorCases(t *testing.T) {
	for _, dir := range getDirs(t, negativeTestDir) {
		t.Run(dir, func(t *testing.T) {
			dockerfile := filepath.Join(negativeTestDir, dir, "Dockerfile")

			df, err := os.Open(dockerfile)
			require.NoError(t, err, dockerfile)
			defer df.Close()

			_, err = Parse(df)
			require.Error(t, err, dockerfile)
		})
	}
}

func TestParseCases(t *testing.T) {
	for _, dir := range getDirs(t, testDir) {
		t.Run(dir, func(t *testing.T) {
			dockerfile := filepath.Join(testDir, dir, "Dockerfile")
			resultfile := filepath.Join(testDir, dir, "result")

			df, err := os.Open(dockerfile)
			require.NoError(t, err, dockerfile)
			defer df.Close()

			result, err := Parse(df)
			require.NoError(t, err, dockerfile)

			content, err := ioutil.ReadFile(resultfile)
			require.NoError(t, err, resultfile)

			if runtime.GOOS == "windows" {
				// CRLF --> CR to match Unix behavior
				content = bytes.Replace(content, []byte{'\x0d', '\x0a'}, []byte{'\x0a'}, -1)
			}
			require.Equal(t, string(content), result.AST.Dump()+"\n", dockerfile)
		})
	}
}

func TestParseWords(t *testing.T) {
	tests := []map[string][]string{
		{
			"input":  {"foo"},
			"expect": {"foo"},
		},
		{
			"input":  {"foo bar"},
			"expect": {"foo", "bar"},
		},
		{
			"input":  {"foo\\ bar"},
			"expect": {"foo\\ bar"},
		},
		{
			"input":  {"foo=bar"},
			"expect": {"foo=bar"},
		},
		{
			"input":  {"foo bar 'abc xyz'"},
			"expect": {"foo", "bar", "'abc xyz'"},
		},
		{
			"input":  {`foo bar "abc xyz"`},
			"expect": {"foo", "bar", `"abc xyz"`},
		},
		{
			"input":  {"àöû"},
			"expect": {"àöû"},
		},
		{
			"input":  {`föo bàr "âbc xÿz"`},
			"expect": {"föo", "bàr", `"âbc xÿz"`},
		},
	}

	for _, test := range tests {
		words := parseWords(test["input"][0], newDefaultDirectives())
		require.Equal(t, test["expect"], words)
	}
}

func TestParseIncludesLineNumbers(t *testing.T) {
	df, err := os.Open(testFileLineInfo)
	require.NoError(t, err)
	defer df.Close()

	result, err := Parse(df)
	require.NoError(t, err)

	ast := result.AST
	require.Equal(t, 5, ast.StartLine)
	require.Equal(t, 31, ast.EndLine)
	require.Equal(t, 3, len(ast.Children))
	expected := [][]int{
		{5, 5},
		{11, 12},
		{17, 31},
	}
	for i, child := range ast.Children {
		msg := fmt.Sprintf("Child %d", i)
		require.Equal(t, expected[i], []int{child.StartLine, child.EndLine}, msg)
	}
}

func TestParsePreservesEmptyCommentLines(t *testing.T) {
	dockerfile := bytes.NewBufferString(`
# This multi-line comment contains an intentionally empty line.
#
# This is the last line of the comment.
FROM image
RUN something
ENTRYPOINT	["/bin/app"]`)

	result, err := Parse(dockerfile)
	require.NoError(t, err)
	ast := result.AST

	// Sanity check, we assume comments are attached to the `FROM` Node
	require.Equal(t, "FROM image", ast.Children[0].Original)

	fromNode := ast.Children[0]
	commentLines := fromNode.PrevComment
	require.Equal(t, 3, len(commentLines))
	require.Equal(t, commentLines[0], "This multi-line comment contains an intentionally empty line.")
	require.Equal(t, commentLines[1], "")
	require.Equal(t, commentLines[2], "This is the last line of the comment.")
}

func TestParseWarnsOnEmptyContinutationLine(t *testing.T) {
	dockerfile := bytes.NewBufferString(`
FROM alpine:3.6

RUN valid \
    continuation

RUN something \

    following \

    more

RUN another \

    thing

RUN non-indented \
# this is a comment
   after-comment

RUN indented \
    # this is an indented comment
    comment
	`)

	result, err := Parse(dockerfile)
	require.NoError(t, err)
	warnings := result.Warnings
	require.Equal(t, 3, len(warnings))
	require.Contains(t, warnings[0], "Empty continuation line found in")
	require.Contains(t, warnings[0], "RUN something     following     more")
	require.Contains(t, warnings[1], "RUN another     thing")
	require.Contains(t, warnings[2], "will become errors in a future release")
}

func TestParseReturnsScannerErrors(t *testing.T) {
	label := strings.Repeat("a", bufio.MaxScanTokenSize)

	dockerfile := strings.NewReader(fmt.Sprintf(`
		FROM image
		LABEL test=%s
`, label))
	_, err := Parse(dockerfile)
	require.EqualError(t, err, "dockerfile line greater than max allowed size of 65535")
}
