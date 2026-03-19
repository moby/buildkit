package pathutil

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeFileName(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		in   string
		want string
	}

	tests := []testCase{
		{name: "simple", in: "foo", want: "foo"},
		{name: "simple_ext", in: "foo.txt", want: "foo.txt"},
		{name: "unicode_cjk", in: "資料.txt", want: "資料.txt"},
		{name: "unicode_cyrillic", in: "тест-файл", want: "тест-файл"},
		{name: "spaces_allowed", in: "name with spaces.txt", want: "name with spaces.txt"},
		{name: "trim_outer_whitespace", in: "  foo.txt  ", want: "foo.txt"},
		{name: "unix_path", in: "a/b/c.txt", want: "c.txt"},
		{name: "empty", in: "", want: "download"},
		{name: "dot", in: ".", want: "download"},
		{name: "dot_dot", in: "..", want: "download"},
		{name: "traversal_unix", in: "../", want: "download"},
		{name: "nul_byte", in: "a\x00b", want: "download"},
		{name: "control", in: "a\nb", want: "download"},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests,
			testCase{name: "windows_traversal", in: "..\\", want: "download"},
			testCase{name: "windows_path_basename", in: "a\\b\\c.txt", want: "c.txt"},
		)
	} else {
		tests = append(tests,
			testCase{name: "windows_traversal_literal", in: "..\\", want: "..\\"},
			testCase{name: "windows_path_literal", in: "a\\b\\c.txt", want: "a\\b\\c.txt"},
		)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, SafeFileName(tt.in))
		})
	}
}
