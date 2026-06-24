package sshutil

import "testing"

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
			if got := knownHostsServerID(tt.hostname, tt.port); got != tt.want {
				t.Fatalf("knownHostsServerID(%q, %q) = %q, want %q", tt.hostname, tt.port, got, tt.want)
			}
		})
	}
}
