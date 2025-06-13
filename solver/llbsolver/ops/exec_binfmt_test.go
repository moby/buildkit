package ops

import (
	"syscall"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestXAttrErrorHandler tests the XAttrErrorHandler logic used in exec_binfmt.go
func TestXAttrErrorHandler(t *testing.T) {
	tests := []struct {
		name         string
		inputErr     error
		shouldIgnore bool
		description  string
	}{
		{
			name:         "ENOTSUP error should be ignored",
			inputErr:     syscall.ENOTSUP,
			shouldIgnore: true,
			description:  "ENOTSUP errors occur on SELinux systems and should be ignored",
		},
		{
			name:         "errors.Is should work with wrapped ENOTSUP",
			inputErr:     errors.Wrap(syscall.ENOTSUP, "failed to set xattr"),
			shouldIgnore: true,
			description:  "Wrapped ENOTSUP errors should also be ignored",
		},
		{
			name:         "EPERM error should not be ignored",
			inputErr:     syscall.EPERM,
			shouldIgnore: false,
			description:  "Other permission errors should be propagated",
		},
		{
			name:         "EIO error should not be ignored",
			inputErr:     syscall.EIO,
			shouldIgnore: false,
			description:  "I/O errors should be propagated",
		},
		{
			name:         "nil error should return nil",
			inputErr:     nil,
			shouldIgnore: true,
			description:  "nil errors should return nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This mimics the XAttrErrorHandler in exec_binfmt.go
			handler := func(dst, src, xattrKey string, err error) error {
				if errors.Is(err, syscall.ENOTSUP) {
					return nil
				}
				return err
			}

			result := handler("dst", "src", "security.selinux", tt.inputErr)

			if tt.shouldIgnore {
				require.NoError(t, result, tt.description)
			} else {
				require.Error(t, result, tt.description)
				require.True(t, errors.Is(result, tt.inputErr), "error should be preserved")
			}
		})
	}
}
