package nvidia

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	in := `NVRM version: NVIDIA UNIX aarch64 Kernel Module  550.120  Fri Sep 13 11:01:10 UTC 2024`
	out, err := parseVersion(in)
	require.NoError(t, err)
	require.Equal(t, "550.120", out)

	in = `NVRM version: NVIDIA UNIX Open Kernel Module for aarch64  550.144.03  Release Build  (dvs-builder@U16-I2-C01-12-4)  Mon Dec 30 17:33:24 UTC 2024
GCC version:  gcc version 12.3.0 (Ubuntu 12.3.0-1ubuntu1~22.04)`
	out, err = parseVersion(in)
	require.NoError(t, err)
	require.Equal(t, "550.144", out)
}

func TestInstallKey(t *testing.T) {
	key := []byte(`-----BEGIN PGP PUBLIC KEY BLOCK-----

mDMEatXZgRYJKwYBBAHaRw8BAQdAmmq72JbhylyD0J6QYRbUMfDtrHJj5tFhR91+
GAVzy9C0NUJ1aWxkS2l0IGtleSBpbnN0YWxsYXRpb24gdGVzdCA8dGVzdEBleGFt
cGxlLmludmFsaWQ+iJAEExYKADgWIQSOsd5+aLB/ocBuGY/fmeLQ7xawGAUCatXZ
gQIbAwULCQgHAgYVCgkICwIEFgIDAQIeAQIXgAAKCRDfmeLQ7xawGDxnAQDxYZMM
lyqptTcvr048d3noCNdG+a9BLtS4AGGpZhBaxwD9H7gjTUpIrxApOqsMkvosvkEs
T9BuitVuAwMSgakGngs=
=aYPn
-----END PGP PUBLIC KEY BLOCK-----
`)
	t.Setenv("GNUPGHOME", t.TempDir())
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	const keyURL = "https://example.invalid/key.pub"

	for _, tc := range []struct {
		name       string
		status     int
		body       []byte
		existing   []byte
		noGPG      bool
		directory  bool
		missingDir bool
		retry      bool
		wantErr    string
	}{
		{
			name:   "success",
			status: http.StatusOK,
			body:   key,
		},
		{
			name:    "HTTP not found",
			status:  http.StatusNotFound,
			body:    []byte("not found"),
			wantErr: "https://example.invalid/key.pub: 404 Not Found",
		},
		{
			name:     "HTTP failure preserves existing key",
			status:   http.StatusServiceUnavailable,
			body:     []byte("unavailable"),
			existing: []byte("existing key"),
			wantErr:  "503 Service Unavailable",
		},
		{
			name:    "invalid key can be retried",
			status:  http.StatusOK,
			body:    []byte("invalid key"),
			retry:   true,
			wantErr: "failed to import NVIDIA GPG key",
		},
		{
			name:    "empty key can be retried",
			status:  http.StatusOK,
			retry:   true,
			wantErr: "failed to import NVIDIA GPG key",
		},
		{
			name:     "invalid key preserves existing key",
			status:   http.StatusOK,
			body:     []byte("invalid key"),
			existing: []byte("existing key"),
			wantErr:  "failed to import NVIDIA GPG key",
		},
		{
			name:    "GPG cannot start",
			status:  http.StatusOK,
			body:    key,
			noGPG:   true,
			wantErr: "failed to import NVIDIA GPG key",
		},
		{
			name:      "rename fails",
			status:    http.StatusOK,
			body:      key,
			directory: true,
			wantErr:   "failed to install NVIDIA GPG key",
		},
		{
			name:       "temporary file creation fails",
			status:     http.StatusOK,
			body:       key,
			missingDir: true,
			wantErr:    "failed to create NVIDIA GPG key file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.status == http.StatusOK && !tc.noGPG && !tc.missingDir {
				if _, err := exec.LookPath("gpg"); err != nil {
					t.Skip("gpg is required")
				}
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "keyring.gpg")
			if tc.missingDir {
				target = filepath.Join(dir, "missing", "keyring.gpg")
			}
			if tc.existing != nil {
				require.NoError(t, os.WriteFile(target, tc.existing, 0644))
			}
			if tc.directory {
				require.NoError(t, os.Mkdir(target, 0700))
			}
			if tc.noGPG {
				t.Setenv("PATH", "")
			}
			body := &keyResponseBody{Reader: bytes.NewReader(tc.body)}
			http.DefaultClient = &http.Client{Transport: keyTransport(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, keyURL, req.URL.String())
				return &http.Response{
					StatusCode: tc.status,
					Status:     fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)),
					Body:       body,
				}, nil
			})}
			err := installKey(t.Context(), keyURL, target, io.Discard)
			require.True(t, body.closed)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				switch {
				case tc.directory:
					info, err := os.Stat(target)
					require.NoError(t, err)
					require.True(t, info.IsDir())
				case tc.existing != nil:
					data, err := os.ReadFile(target)
					require.NoError(t, err)
					require.Equal(t, tc.existing, data)
				default:
					_, err := os.Stat(target)
					require.ErrorIs(t, err, os.ErrNotExist)
				}
				if tc.retry {
					body = &keyResponseBody{Reader: bytes.NewReader(key)}
					require.NoError(t, installKey(t.Context(), keyURL, target, io.Discard))
					require.True(t, body.closed)
				}
			} else {
				require.NoError(t, err)
			}
			if tc.wantErr == "" || tc.retry {
				data, err := os.ReadFile(target)
				require.NoError(t, err)
				require.NotEmpty(t, data)
				require.NotEqual(t, key, data)
				if runtime.GOOS != "windows" {
					info, err := os.Stat(target)
					require.NoError(t, err)
					require.Equal(t, os.FileMode(0644), info.Mode().Perm())
				}
			}
			leftovers, err := filepath.Glob(filepath.Join(dir, ".nvidia-key-*"))
			require.NoError(t, err)
			require.Empty(t, leftovers)
		})
	}
}

func TestInstallKeyDownloadFailure(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	downloadErr := errors.New("download failed")
	http.DefaultClient = &http.Client{Transport: keyTransport(func(*http.Request) (*http.Response, error) {
		return nil, downloadErr
	})}
	target := filepath.Join(t.TempDir(), "keyring.gpg")
	err := installKey(t.Context(), "https://example.invalid/key.pub", target, io.Discard)
	require.ErrorIs(t, err, downloadErr)
	_, err = os.Stat(target)
	require.ErrorIs(t, err, os.ErrNotExist)
}

type keyTransport func(*http.Request) (*http.Response, error)

func (f keyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type keyResponseBody struct {
	io.Reader
	closed bool
}

func (b *keyResponseBody) Close() error {
	b.closed = true
	return nil
}
