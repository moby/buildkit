package resolvconf

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allow running "go test -v -update ." to update golden files.
var updateGolden = flag.Bool("update", false, "update golden files")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func assertGolden(t *testing.T, goldenFile string, actual string) {
	t.Helper()
	goldenFile = filepath.Join("testdata", goldenFile)

	if *updateGolden {
		require.NoError(t, os.WriteFile(goldenFile, []byte(actual), 0644))
	}
	expected, err := os.ReadFile(goldenFile)
	require.NoError(t, err)
	assert.Equal(t, string(expected), actual)
}

// taken from https://github.com/moby/moby/blob/9af9d2742cc751c38900efaa385968c75ff3fdd7/internal/sliceutil/sliceutil.go#L26-L34
func sliceutilMapper[In, Out any](fn func(In) Out) func([]In) []Out {
	return func(s []In) []Out {
		res := make([]Out, len(s))
		for i, v := range s {
			res[i] = fn(v)
		}
		return res
	}
}
