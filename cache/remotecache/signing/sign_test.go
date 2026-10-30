package signing

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

type verifierFunc func(context.Context, digest.Digest, []byte) error

func (f verifierFunc) Verify(ctx context.Context, dgst digest.Digest, bundle []byte) error {
	return f(ctx, dgst, bundle)
}

func signingTestCommand(t *testing.T, mode string, args ...string) []string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	return append([]string{executable, "-test.run=^TestSigningCommand$", "--", "signing-test-helper", mode}, args...)
}

// TestSigningCommand runs in a subprocess to exercise the actual command I/O.
func TestSigningCommand(t *testing.T) {
	marker := slices.Index(os.Args, "signing-test-helper")
	if marker == -1 {
		return
	}
	args := os.Args[marker+1:]
	switch args[0] {
	case "echo":
		if len(args) != 2 || args[1] != "literal argument with spaces * $HOME" {
			os.Exit(2)
		}
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(2)
		}
	case "fail":
		fmt.Fprint(os.Stderr, "signer failed")
		os.Exit(3)
	case "wait":
		if err := os.WriteFile(args[1], nil, 0600); err != nil { //nolint:gosec // path supplied by the parent test in its temporary directory
			os.Exit(2)
		}
		time.Sleep(time.Minute)
	default:
		os.Exit(2)
	}
	// Avoid the test runner's PASS output becoming part of the bundle.
	os.Exit(0)
}

func TestSigner(t *testing.T) {
	payload := []byte("{\n  \"records\": []\n}\n")
	verifyErr := errors.New("signature rejected")
	for _, tc := range []struct {
		name      string
		verifyErr error
	}{
		{
			name: "verified output",
		},
		{
			name:      "rejected output",
			verifyErr: verifyErr,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			command := signingTestCommand(t, "echo", "literal argument with spaces * $HOME")
			s := NewSigner(command, verifierFunc(func(ctx context.Context, dgst digest.Digest, bundle []byte) error {
				calls++
				require.Equal(t, t.Context(), ctx)
				require.Equal(t, digest.FromBytes(payload), dgst)
				require.Equal(t, payload, bundle, "payload bytes must not be reserialized")
				return tc.verifyErr
			}))
			command[0] = "changed-after-construction"
			bundle, err := s.Sign(t.Context(), payload)
			require.Equal(t, 1, calls)
			if tc.verifyErr != nil {
				require.ErrorIs(t, err, tc.verifyErr)
				require.Nil(t, bundle)
			} else {
				require.NoError(t, err)
				require.Equal(t, payload, bundle)
			}
		})
	}
}

func TestSignerCommandFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
		wantErr string
	}{
		{
			name:    "no command",
			wantErr: "no signing command configured",
		},
		{
			name:    "missing executable",
			command: []string{filepath.Join(t.TempDir(), "missing-signer")},
			wantErr: "signing command failed",
		},
		{
			name:    "nonzero exit",
			command: signingTestCommand(t, "fail"),
			wantErr: "signing command failed: signer failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSigner(tc.command, verifierFunc(func(context.Context, digest.Digest, []byte) error {
				t.Fatal("must not verify output from a failed command")
				return nil
			}))
			bundle, err := s.Sign(t.Context(), []byte("payload"))
			require.ErrorContains(t, err, tc.wantErr)
			require.Nil(t, bundle)
		})
	}
}

func TestSignerWithoutVerifier(t *testing.T) {
	s := NewSigner([]string{"must-not-run"}, nil)
	bundle, err := s.Sign(t.Context(), nil)
	require.ErrorContains(t, err, "no verifier available")
	require.Nil(t, bundle)
}

func TestSignerCancellation(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	s := NewSigner(signingTestCommand(t, "wait", started), verifierFunc(func(context.Context, digest.Digest, []byte) error {
		t.Error("must not verify output from a canceled command")
		return nil
	}))
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	done := make(chan error, 1)
	go func() {
		_, err := s.Sign(ctx, []byte("payload"))
		done <- err
	}()
	require.Eventually(t, func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
	cancel(context.Canceled)
	select {
	case err := <-done:
		require.ErrorContains(t, err, "signing command failed")
	case <-time.After(10 * time.Second):
		t.Fatal("signing command did not exit after cancellation")
	}
}
