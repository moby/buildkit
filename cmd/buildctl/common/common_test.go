package common_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/moby/buildkit/cmd/buildctl/common"
)

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestResolveTLSFilesFromDir(t *testing.T) {
	t.Run("all files present for cert-manager style", func(t *testing.T) {
		dir := t.TempDir()
		ca := writeTempFile(t, dir, "ca.crt", "ca")
		cert := writeTempFile(t, dir, "tls.crt", "cert")
		key := writeTempFile(t, dir, "tls.key", "key")

		caOut, certOut, keyOut := common.ResolveTLSFilesFromDir(dir)
		require.Equal(t, ca, caOut)
		require.Equal(t, cert, certOut)
		require.Equal(t, key, keyOut)
	})

	t.Run("all files present for pem style", func(t *testing.T) {
		dir := t.TempDir()
		ca := writeTempFile(t, dir, "ca.pem", "ca")
		cert := writeTempFile(t, dir, "cert.pem", "cert")
		key := writeTempFile(t, dir, "key.pem", "key")

		caOut, certOut, keyOut := common.ResolveTLSFilesFromDir(dir)
		require.Equal(t, ca, caOut)
		require.Equal(t, cert, certOut)
		require.Equal(t, key, keyOut)
	})

	t.Run("some files missing", func(t *testing.T) {
		dir := t.TempDir()
		ca := writeTempFile(t, dir, "ca.crt", "ca")
		// cert and key missing

		caOut, certOut, keyOut := common.ResolveTLSFilesFromDir(dir)
		require.Equal(t, ca, caOut)
		require.Empty(t, certOut)
		require.Empty(t, keyOut)
	})
}
