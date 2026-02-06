package build

import (
	"testing"
)

func TestParseAutomount(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected map[string]string
	}{
		{
			name:     "empty",
			input:    nil,
			expected: nil,
		},
		{
			name:     "single automount",
			input:    []string{"type=bind,source=ca.crt,target=/etc/ssl/certs/ca-certificates.crt"},
			expected: map[string]string{"automount:0": "type=bind,source=ca.crt,target=/etc/ssl/certs/ca-certificates.crt"},
		},
		{
			name:  "multiple automounts",
			input: []string{"type=secret,id=https_proxy,env=HTTPS_PROXY", "type=bind,source=proxy.crt,target=/etc/ssl/certs/ca-certificates.crt"},
			expected: map[string]string{
				"automount:0": "type=secret,id=https_proxy,env=HTTPS_PROXY",
				"automount:1": "type=bind,source=proxy.crt,target=/etc/ssl/certs/ca-certificates.crt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseAutomount(tt.input)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d entries, got %d", len(tt.expected), len(result))
			}

			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("expected result[%q] = %q, got %q", k, v, result[k])
				}
			}
		})
	}
}
