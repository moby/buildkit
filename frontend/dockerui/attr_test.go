package dockerui

import (
	"reflect"
	"testing"
)

func TestFilterValues(t *testing.T) {
	tests := []struct {
		name     string
		opt      map[string]string
		prefix   string
		expected []string
	}{
		{
			name:     "empty map",
			opt:      map[string]string{},
			prefix:   "automount:",
			expected: nil,
		},
		{
			name: "no matching prefix",
			opt: map[string]string{
				"build-arg:FOO": "bar",
			},
			prefix:   "automount:",
			expected: nil,
		},
		{
			name: "single match",
			opt: map[string]string{
				"automount:0": "type=bind,source=ca.crt,target=/etc/ssl/certs/ca-certificates.crt",
			},
			prefix:   "automount:",
			expected: []string{"type=bind,source=ca.crt,target=/etc/ssl/certs/ca-certificates.crt"},
		},
		{
			name: "multiple matches sorted",
			opt: map[string]string{
				"automount:2": "third",
				"automount:0": "first",
				"automount:1": "second",
			},
			prefix:   "automount:",
			expected: []string{"first", "second", "third"},
		},
		{
			name: "mixed with other prefixes",
			opt: map[string]string{
				"build-arg:FOO": "bar",
				"automount:1":   "second",
				"automount:0":   "first",
				"label:app":     "myapp",
			},
			prefix:   "automount:",
			expected: []string{"first", "second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterValues(tt.opt, tt.prefix)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}
