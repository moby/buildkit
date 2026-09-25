package sshutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKnownHostsServerID(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		port     string
		want     string
	}{
		{"default port renders bare host", "github.com", "22", "github.com"},
		{"empty port treated as default", "github.com", "", "github.com"},
		{"non-standard port is bracketed", "git.example.com", "2222", "[git.example.com]:2222"},
		{"ipv6 default port", "::1", "22", "::1"},
		{"ipv6 non-standard port is bracketed", "::1", "2222", "[::1]:2222"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equalf(t, tt.want, knownHostsServerID(tt.hostname, tt.port), "knownHostsServerID(%q, %q)", tt.hostname, tt.port)
		})
	}
}
