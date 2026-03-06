package local

import (
	"testing"

	"github.com/moby/buildkit/session/filesync"
	"github.com/stretchr/testify/require"
)

func TestCreateFSOptsLoadModeDefault(t *testing.T) {
	var opts CreateFSOpts
	_, err := opts.Load(nil)
	require.NoError(t, err)
	require.Equal(t, filesync.FSSyncDirModeCopy, opts.Mode)
}

func TestCreateFSOptsLoadModeMirror(t *testing.T) {
	var opts CreateFSOpts
	_, err := opts.Load(map[string]string{
		"mode": "mirror",
	})
	require.NoError(t, err)
	require.Equal(t, filesync.FSSyncDirModeMirror, opts.Mode)
}

func TestCreateFSOptsLoadModeInvalid(t *testing.T) {
	var opts CreateFSOpts
	_, err := opts.Load(map[string]string{
		"mode": "backup",
	})
	require.Error(t, err)
	require.ErrorContains(t, err, `invalid local exporter mode "backup"`)
}
