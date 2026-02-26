package pb

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golang/protobuf/proto" //nolint:staticcheck
)

var record = flag.Bool("record", false, "record actual values as fixtures")

func TestProto(t *testing.T) {
	// Because of stable_marshaler_all, the map will be sorted in the result binary.
	r := &SourceOp{
		Identifier: "docker-image://docker.io/library/foo:latest",
		Attrs: map[string]string{
			"a": "foo",
			"b": "bar",
			"c": "baz",
		},
	}

	gogo, err := r.Marshal()
	require.NoError(t, err)
	assertProto(t, gogo, "SourceOp")

	google, err := proto.Marshal(r)
	require.NoError(t, err)
	assertProto(t, google, "SourceOp")
}

func assertProto(tb testing.TB, actual []byte, name string) {
	path := fmt.Sprintf("testdata/%s.bin", name)
	if *record {
		err := os.WriteFile(path, actual, 0600)
		require.NoError(tb, err)
		return
	}
	expected, err := os.ReadFile(path)
	require.NoError(tb, err)
	assert.Equal(tb, expected, actual)
}
