//go:build linux

package executor

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInjectProxyCACleanupPreservesContainerChanges(t *testing.T) {
	original := []byte("original bundle\n")
	bundle, cleanup := injectTestProxyCA(t, original)

	dt, err := os.ReadFile(bundle)
	require.NoError(t, err)

	change := []byte("container change\n")
	require.NoError(t, os.WriteFile(bundle, append(dt, change...), 0o644))
	require.NoError(t, cleanup())

	dt, err = os.ReadFile(bundle)
	require.NoError(t, err)
	expected := append(append([]byte{}, original...), change...)
	require.Equal(t, expected, dt)
}

func TestInjectProxyCARestoresBundleWithoutTrailingNewline(t *testing.T) {
	original := []byte("original bundle")
	bundle, cleanup := injectTestProxyCA(t, original)

	require.NoError(t, cleanup())

	dt, err := os.ReadFile(bundle)
	require.NoError(t, err)
	require.Equal(t, original, dt)
}

func TestInjectProxyCARestoresEmptyBundle(t *testing.T) {
	original := []byte{}
	bundle, cleanup := injectTestProxyCA(t, original)

	require.NoError(t, cleanup())

	dt, err := os.ReadFile(bundle)
	require.NoError(t, err)
	require.Equal(t, original, dt)
}

func TestInjectProxyCARestoresBundleEndingInNewline(t *testing.T) {
	original := []byte("original bundle\n")
	bundle, cleanup := injectTestProxyCA(t, original)

	require.NoError(t, cleanup())

	dt, err := os.ReadFile(bundle)
	require.NoError(t, err)
	require.Equal(t, original, dt)
}

func TestInjectProxyCACleanupAllowsDeletedBundle(t *testing.T) {
	bundle, cleanup := injectTestProxyCA(t, []byte("original bundle\n"))
	require.NoError(t, os.Remove(bundle))

	require.NoError(t, cleanup())
	require.NoFileExists(t, bundle)
}

func TestInjectProxyCACleanupRejectsOversizedBundle(t *testing.T) {
	bundle, cleanup := injectTestProxyCA(t, []byte("original bundle\n"))
	require.NoError(t, os.Truncate(bundle, maxCertBundleBytes+1))

	err := cleanup()
	require.Error(t, err)
	require.ErrorContains(t, err, "exceeds 10485760 bytes")
}

func injectTestProxyCA(t *testing.T, original []byte) (string, func() error) {
	t.Helper()
	rootfs := t.TempDir()
	bundle := filepath.Join(rootfs, "etc/ssl/certs/ca-certificates.crt")
	require.NoError(t, os.MkdirAll(filepath.Dir(bundle), 0o755))
	require.NoError(t, os.WriteFile(bundle, original, 0o644))

	cleanup, err := InjectProxyCA(rootfs, testCertPEM(t))
	require.NoError(t, err)
	return bundle, cleanup
}

func testCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test buildkit proxy"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
