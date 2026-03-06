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

func TestCreateFSOptsLoadModeDelete(t *testing.T) {
	var opts CreateFSOpts
	_, err := opts.Load(map[string]string{
		"mode": "delete",
	})
	require.NoError(t, err)
	require.Equal(t, filesync.FSSyncDirModeDelete, opts.Mode)
}

func TestCreateFSOptsLoadModeInvalid(t *testing.T) {
	var opts CreateFSOpts
	_, err := opts.Load(map[string]string{
		"mode": "backup",
	})
	require.Error(t, err)
	require.ErrorContains(t, err, `invalid local exporter mode "backup"`)
}
