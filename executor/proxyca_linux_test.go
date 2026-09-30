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
	"syscall"
	"testing"
	"time"

	"github.com/moby/buildkit/util/openfile"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestInjectProxyCACleanupPreservesContainerChanges(t *testing.T) {
	rootfs := t.TempDir()
	root, err := os.OpenRoot(rootfs)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, root.Close())
	})
	const bundle = "etc/ssl/certs/ca-certificates.crt"
	require.NoError(t, root.MkdirAll(filepath.Dir(bundle), 0o755))
	original := []byte("original bundle\n")
	require.NoError(t, root.WriteFile(bundle, original, 0o644))

	caPEM := testCertPEM(t)
	cleanup, err := InjectProxyCA(rootfs, caPEM)
	require.NoError(t, err)

	dt, err := root.ReadFile(bundle)
	require.NoError(t, err)
	require.Contains(t, string(dt), string(caPEM))

	require.NoError(t, root.WriteFile(bundle, append(dt, []byte("container change\n")...), 0o644))
	require.NoError(t, cleanup())

	dt, err = root.ReadFile(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(dt), string(caPEM))
	require.Contains(t, string(dt), string(original))
	require.Contains(t, string(dt), "container change\n")
}

func TestInjectProxyCACleanupRestoresBundleExactly(t *testing.T) {
	for name, original := range map[string][]byte{
		"empty":                    {},
		"without trailing newline": []byte("original bundle"),
		"with trailing newline":    []byte("original bundle\n"),
	} {
		t.Run(name, func(t *testing.T) {
			rootfs := t.TempDir()
			root, err := os.OpenRoot(rootfs)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, root.Close())
			})

			const bundle = "etc/ssl/certs/ca-certificates.crt"
			require.NoError(t, root.MkdirAll(filepath.Dir(bundle), 0o755))
			require.NoError(t, root.WriteFile(bundle, original, 0o644))

			cleanup, err := InjectProxyCA(rootfs, testCertPEM(t))
			require.NoError(t, err)
			require.NoError(t, cleanup())

			dt, err := root.ReadFile(bundle)
			require.NoError(t, err)
			require.Equal(t, original, dt)
		})
	}
}

func TestInjectProxyCACleanupHandlesRetargetedSymlinks(t *testing.T) {
	t.Run("external directory symlink", func(t *testing.T) {
		rootfs := t.TempDir()
		const testCABundle = "etc/ssl/certs/ca-certificates.crt"
		bundle := filepath.Join(rootfs, testCABundle)
		certsDir := filepath.Join(rootfs, "etc/ssl/certs")
		require.NoError(t, os.MkdirAll(certsDir, 0o755))
		require.NoError(t, os.WriteFile(bundle, []byte("original bundle\n"), 0o644))

		caPEM := testCertPEM(t)
		cleanup, err := InjectProxyCA(rootfs, caPEM)
		require.NoError(t, err)

		external := t.TempDir()
		externalRoot, err := os.OpenRoot(external)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, externalRoot.Close())
		})
		const externalBundle = "ca-certificates.crt"
		injected, err := os.ReadFile(bundle)
		require.NoError(t, err)
		require.Contains(t, string(injected), string(caPEM))
		require.NoError(t, externalRoot.WriteFile(externalBundle, injected, 0o644))

		require.NoError(t, os.RemoveAll(certsDir))
		require.NoError(t, os.Symlink(external, certsDir))
		require.NoError(t, cleanup())

		after, err := externalRoot.ReadFile(externalBundle)
		require.NoError(t, err)
		require.Equal(t, string(injected), string(after))
		require.Contains(t, string(after), string(caPEM))
	})

	t.Run("retargeted bundle symlink", func(t *testing.T) {
		rootfs := t.TempDir()
		const testCABundle = "etc/ssl/certs/ca-certificates.crt"
		bundle := filepath.Join(rootfs, testCABundle)
		require.NoError(t, os.MkdirAll(filepath.Dir(bundle), 0o755))

		originalTarget := filepath.Join(rootfs, "original/ca-bundle.crt")
		original := []byte("original bundle\n")
		require.NoError(t, os.MkdirAll(filepath.Dir(originalTarget), 0o755))
		require.NoError(t, os.WriteFile(originalTarget, original, 0o644))

		retargetedTarget := filepath.Join(rootfs, "retargeted/ca-bundle.crt")
		retargeted := []byte("retargeted bundle\n")
		require.NoError(t, os.MkdirAll(filepath.Dir(retargetedTarget), 0o755))
		require.NoError(t, os.WriteFile(retargetedTarget, retargeted, 0o644))

		require.NoError(t, os.Symlink("/original/ca-bundle.crt", bundle))

		caPEM := testCertPEM(t)
		cleanup, err := InjectProxyCA(rootfs, caPEM)
		require.NoError(t, err)

		injected, err := os.ReadFile(originalTarget)
		require.NoError(t, err)
		require.Contains(t, string(injected), string(caPEM))

		require.NoError(t, os.Remove(bundle))
		require.NoError(t, os.Symlink("/retargeted/ca-bundle.crt", bundle))
		require.NoError(t, cleanup())

		afterOriginal, err := os.ReadFile(originalTarget)
		require.NoError(t, err)
		require.NotContains(t, string(afterOriginal), string(caPEM))
		require.Contains(t, string(afterOriginal), string(original))

		afterRetargeted, err := os.ReadFile(retargetedTarget)
		require.NoError(t, err)
		require.Equal(t, string(retargeted), string(afterRetargeted))
	})
}

func TestInjectProxyCACleanupDoesNotBlockOnFIFO(t *testing.T) {
	rootfs := t.TempDir()
	const bundle = "etc/ssl/certs/ca-certificates.crt"
	bundlePath := filepath.Join(rootfs, bundle)
	require.NoError(t, os.MkdirAll(filepath.Dir(bundlePath), 0o755))
	require.NoError(t, os.WriteFile(bundlePath, []byte("original bundle\n"), 0o644))

	caPEM := testCertPEM(t)
	cleanup, err := InjectProxyCA(rootfs, caPEM)
	require.NoError(t, err)

	// Simulate a malicious RUN step replacing the bundle with a FIFO that has
	// no writer. A blocking open would hang cleanup indefinitely.
	require.NoError(t, os.Remove(bundlePath))
	require.NoError(t, syscall.Mkfifo(bundlePath, 0o644))

	done := make(chan error, 1)
	go func() {
		done <- cleanup()
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, openfile.ErrNotRegular)
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup blocked on FIFO bundle")
	}
}

func TestInjectProxyCACleanupRejectsDeviceNode(t *testing.T) {
	rootfs := t.TempDir()
	const bundle = "etc/ssl/certs/ca-certificates.crt"
	bundlePath := filepath.Join(rootfs, bundle)
	require.NoError(t, os.MkdirAll(filepath.Dir(bundlePath), 0o755))
	require.NoError(t, os.WriteFile(bundlePath, []byte("original bundle\n"), 0o644))

	cleanup, err := InjectProxyCA(rootfs, testCertPEM(t))
	require.NoError(t, err)
	require.NoError(t, os.Remove(bundlePath))
	// Use /dev/null so the test remains harmless if the safety check regresses.
	if err := unix.Mknod(bundlePath, unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))); err != nil {
		t.Skipf("cannot create device node: %v", err)
	}

	err = cleanup()
	require.ErrorIs(t, err, openfile.ErrNotRegular)
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
