package instructions

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDevice(t *testing.T) {
	cases := []struct {
		input       string
		expected    *Device
		expectedErr error
	}{
		{
			input:       "vendor1.com/device=foo",
			expected:    &Device{Name: "vendor1.com/device=foo", Required: false},
			expectedErr: nil,
		},
		{
			input:       "vendor1.com/device",
			expected:    &Device{Name: "vendor1.com/device", Required: false},
			expectedErr: nil,
		},
		{
			input:       "vendor1.com/device=foo,required",
			expected:    &Device{Name: "vendor1.com/device=foo", Required: true},
			expectedErr: nil,
		},
		{
			input:       "vendor1.com/device=foo,required=true",
			expected:    &Device{Name: "vendor1.com/device=foo", Required: true},
			expectedErr: nil,
		},
		{
			input:       "vendor1.com/device=foo,required=false",
			expected:    &Device{Name: "vendor1.com/device=foo", Required: false},
			expectedErr: nil,
		},
		{
			input:       "name=vendor1.com/device=foo",
			expected:    &Device{Name: "vendor1.com/device=foo", Required: false},
			expectedErr: nil,
		},
		{
			input:       "name=vendor1.com/device=foo,required",
			expected:    &Device{Name: "vendor1.com/device=foo", Required: true},
			expectedErr: nil,
		},
		{
			input:       "vendor1.com/device=foo,name=vendor2.com/device=bar",
			expected:    nil,
			expectedErr: errors.New("device name already set to vendor1.com/device=foo"),
		},
	}
	for _, tt := range cases {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseDevice(tt.input)
			if tt.expectedErr != nil {
				require.Error(t, err)
				require.EqualError(t, err, tt.expectedErr.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
			}
		})
	}
}
