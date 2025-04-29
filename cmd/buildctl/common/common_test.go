package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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

		caOut, certOut, keyOut, err := resolveTLSFilesFromDir(dir)
		require.Equal(t, ca, caOut)
		require.Equal(t, cert, certOut)
		require.Equal(t, key, keyOut)
		require.NoError(t, err)
	})

	t.Run("all files present for pem style", func(t *testing.T) {
		dir := t.TempDir()
		ca := writeTempFile(t, dir, "ca.pem", "ca")
		cert := writeTempFile(t, dir, "cert.pem", "cert")
		key := writeTempFile(t, dir, "key.pem", "key")

		caOut, certOut, keyOut, err := resolveTLSFilesFromDir(dir)
		require.Equal(t, ca, caOut)
		require.Equal(t, cert, certOut)
		require.Equal(t, key, keyOut)
		require.NoError(t, err)
	})

	t.Run("mixed set is present", func(t *testing.T) {
		dir := t.TempDir()
		ca := writeTempFile(t, dir, "ca.crt", "ca-cert-manager")
		cert := writeTempFile(t, dir, "cert.pem", "cert-pem")
		key := writeTempFile(t, dir, "key.pem", "key-pem")
		// ca for cert-manager, cert and key for pem

		caOut, certOut, keyOut, err := resolveTLSFilesFromDir(dir)
		require.Equal(t, ca, caOut)
		require.Equal(t, cert, certOut)
		require.Equal(t, key, keyOut)
		require.NoError(t, err)
	})

	t.Run("all files present for cert-manager and pem styles and pem is chosen", func(t *testing.T) {
		dir := t.TempDir()
		writeTempFile(t, dir, "ca.crt", "ca-cert-manager")
		writeTempFile(t, dir, "tls.crt", "cert-cert-manager")
		writeTempFile(t, dir, "tls.key", "key-cert-manager")
		ca := writeTempFile(t, dir, "ca.pem", "ca-pem")
		cert := writeTempFile(t, dir, "cert.pem", "cert-pem")
		key := writeTempFile(t, dir, "key.pem", "key-pem")

		caOut, certOut, keyOut, err := resolveTLSFilesFromDir(dir)
		require.Equal(t, ca, caOut)
		require.Equal(t, cert, certOut)
		require.Equal(t, key, keyOut)
		require.NoError(t, err)
	})
}
