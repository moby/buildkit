package ops

import (
	"syscall"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestBinfmtXAttrErrorHandler(t *testing.T) {
	tests := []struct {
		name        string
		dst         string
		src         string
		xattrKey    string
		inputErr    error
		expectErr   bool
		description string
	}{
		{
			name:        "ENOTSUP_security_selinux_ignored",
			dst:         "/tmp/dest",
			src:         "/tmp/src",
			xattrKey:    "security.selinux",
			inputErr:    syscall.ENOTSUP,
			expectErr:   false,
			description: "ENOTSUP error for security.selinux should be ignored",
		},
		{
			name:        "ENOTSUP_other_xattr_propagated",
			dst:         "/tmp/dest",
			src:         "/tmp/src",
			xattrKey:    "user.some_attr",
			inputErr:    syscall.ENOTSUP,
			expectErr:   true,
			description: "ENOTSUP error for non-selinux xattr should propagate",
		},
		{
			name:        "ENOTSUP_security_capability_propagated",
			dst:         "/tmp/dest",
			src:         "/tmp/src",
			xattrKey:    "security.capability",
			inputErr:    syscall.ENOTSUP,
			expectErr:   true,
			description: "ENOTSUP error for other security xattr should propagate",
		},
		{
			name:        "other_error_security_selinux_propagated",
			dst:         "/tmp/dest",
			src:         "/tmp/src",
			xattrKey:    "security.selinux",
			inputErr:    syscall.EPERM,
			expectErr:   true,
			description: "Non-ENOTSUP errors should always propagate",
		},
		{
			name:        "wrapped_ENOTSUP_error_handled",
			dst:         "/tmp/dest",
			src:         "/tmp/src",
			xattrKey:    "security.selinux",
			inputErr:    errors.Wrap(syscall.ENOTSUP, "wrapped error"),
			expectErr:   false,
			description: "Wrapped ENOTSUP errors should be handled with errors.Is",
		},
		{
			name:        "nil_error_returns_nil",
			dst:         "/tmp/dest",
			src:         "/tmp/src",
			xattrKey:    "security.selinux",
			inputErr:    nil,
			expectErr:   false,
			description: "Nil error should return nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ignoreSELinuxXAttrErrorHandler(tt.dst, tt.src, tt.xattrKey, tt.inputErr)

			if tt.expectErr {
				require.Error(t, result, tt.description)
				require.Equal(t, tt.inputErr, result, "Error should be propagated unchanged")
			} else {
				require.NoError(t, result, tt.description)
			}
		})
	}
}
