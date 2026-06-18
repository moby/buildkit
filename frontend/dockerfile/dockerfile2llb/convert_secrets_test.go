package dockerfile2llb

import (
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestDispatchSecretTarget(t *testing.T) {
	newState := func(os string) *dispatchState {
		return &dispatchState{
			platform: &ocispecs.Platform{OS: os},
			outline:  newOutlineCapture(),
		}
	}

	t.Run("windows requires explicit target", func(t *testing.T) {
		d := newState("windows")
		m := &instructions.Mount{Type: instructions.MountTypeSecret, CacheID: "mysecret"}
		_, err := dispatchSecret(d, m, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "secret target is required on Windows")
	})

	t.Run("windows explicit target ok", func(t *testing.T) {
		d := newState("windows")
		m := &instructions.Mount{Type: instructions.MountTypeSecret, CacheID: "mysecret", Target: "C:/secret.txt"}
		_, err := dispatchSecret(d, m, nil)
		require.NoError(t, err)
	})

	t.Run("windows normalizes backslash target", func(t *testing.T) {
		d := newState("windows")
		m := &instructions.Mount{Type: instructions.MountTypeSecret, CacheID: "mysecret", Target: "C:\\dir\\secret.txt"}
		_, err := dispatchSecret(d, m, nil)
		require.NoError(t, err)
		require.Equal(t, "C:/dir/secret.txt", m.Target)
	})

	t.Run("windows rejects drive-relative target", func(t *testing.T) {
		d := newState("windows")
		m := &instructions.Mount{Type: instructions.MountTypeSecret, CacheID: "mysecret", Target: "C:secret.txt"}
		_, err := dispatchSecret(d, m, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be an absolute path")
	})

	t.Run("linux preserves backslash target", func(t *testing.T) {
		d := newState("linux")
		m := &instructions.Mount{Type: instructions.MountTypeSecret, CacheID: "mysecret", Target: "/a\\b"}
		_, err := dispatchSecret(d, m, nil)
		require.NoError(t, err)
		require.Equal(t, "/a\\b", m.Target)
	})

	t.Run("linux defaults target", func(t *testing.T) {
		d := newState("linux")
		m := &instructions.Mount{Type: instructions.MountTypeSecret, CacheID: "mysecret"}
		_, err := dispatchSecret(d, m, nil)
		require.NoError(t, err)
	})
}
