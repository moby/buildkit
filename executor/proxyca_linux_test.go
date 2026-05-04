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
	rootfs := t.TempDir()
	bundle := filepath.Join(rootfs, "etc/ssl/certs/ca-certificates.crt")
	require.NoError(t, os.MkdirAll(filepath.Dir(bundle), 0o755))
	original := []byte("original bundle\n")
	require.NoError(t, os.WriteFile(bundle, original, 0o644))

	caPEM := testCertPEM(t)
	cleanup, err := InjectProxyCA(rootfs, caPEM)
	require.NoError(t, err)

	dt, err := os.ReadFile(bundle)
	require.NoError(t, err)
	require.Contains(t, string(dt), string(caPEM))

	require.NoError(t, os.WriteFile(bundle, append(dt, []byte("container change\n")...), 0o644))
	require.NoError(t, cleanup())

	dt, err = os.ReadFile(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(dt), string(caPEM))
	require.Contains(t, string(dt), string(original))
	require.Contains(t, string(dt), "container change\n")
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
