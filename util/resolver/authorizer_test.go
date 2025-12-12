package resolver

import (
	"crypto/tls"
	"net/http"
	"reflect"
	"testing"

	"github.com/moby/buildkit/util/tracing"
	"github.com/stretchr/testify/require"
)

func TestParseScopes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    []string
		expected scopes
	}{
		{
			name:     "InvalidScope",
			input:    []string{""},
			expected: nil,
		},
		{
			name: "SeparateStrings",
			input: []string{
				"repository:foo/bar:pull",
				"repository:foo/baz:pull,push",
			},
			expected: map[string]map[string]struct{}{
				"repository:foo/bar": {
					"pull": struct{}{},
				},
				"repository:foo/baz": {
					"pull": struct{}{},
					"push": struct{}{},
				},
			},
		},
		{
			name:  "CombinedStrings",
			input: []string{"repository:foo/bar:pull repository:foo/baz:pull,push"},
			expected: map[string]map[string]struct{}{
				"repository:foo/bar": {
					"pull": struct{}{},
				},
				"repository:foo/baz": {
					"pull": struct{}{},
					"push": struct{}{},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := parseScopes(tc.input)
			if !reflect.DeepEqual(parsed, tc.expected) {
				t.Fatalf("expected %v, got %v", tc.expected, parsed)
			}
		})
	}
}

func TestClientHasMTLS(t *testing.T) {
	tests := []struct {
		name     string
		client   *http.Client
		expected bool
	}{
		{
			name:     "nil client",
			client:   nil,
			expected: false,
		},
		{
			name:     "client with nil transport",
			client:   &http.Client{Transport: nil},
			expected: false,
		},
		{
			name: "client without TLS config",
			client: &http.Client{
				Transport: &http.Transport{},
			},
			expected: false,
		},
		{
			name: "client with empty TLS config",
			client: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{},
				},
			},
			expected: false,
		},
		{
			name: "client with TLS config but no certificates",
			client: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						Certificates: []tls.Certificate{},
					},
				},
			},
			expected: false,
		},
		{
			name: "client with mTLS certificates",
			client: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						Certificates: []tls.Certificate{{}},
					},
				},
			},
			expected: true,
		},
		{
			name: "client with tracing wrapper and mTLS certificates",
			client: &http.Client{
				Transport: tracing.NewTransport(&http.Transport{
					TLSClientConfig: &tls.Config{
						Certificates: []tls.Certificate{{}},
					},
				}),
			},
			expected: true,
		},
		{
			name: "client with tracing wrapper but no mTLS",
			client: &http.Client{
				Transport: tracing.NewTransport(&http.Transport{}),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := clientHasMTLS(tt.client)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestTransportHasMTLS(t *testing.T) {
	tests := []struct {
		name      string
		transport http.RoundTripper
		expected  bool
	}{
		{
			name:      "nil transport",
			transport: nil,
			expected:  false,
		},
		{
			name:      "plain http.Transport without TLS",
			transport: &http.Transport{},
			expected:  false,
		},
		{
			name: "http.Transport with mTLS",
			transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{{}},
				},
			},
			expected: true,
		},
		{
			name: "TracingTransport wrapping http.Transport with mTLS",
			transport: tracing.NewTransport(&http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{{}},
				},
			}),
			expected: true,
		},
		{
			name:      "TracingTransport wrapping http.Transport without mTLS",
			transport: tracing.NewTransport(&http.Transport{}),
			expected:  false,
		},
		{
			name: "httpFallback wrapping TracingTransport with mTLS",
			transport: &httpFallback{
				super: tracing.NewTransport(&http.Transport{
					TLSClientConfig: &tls.Config{
						Certificates: []tls.Certificate{{}},
					},
				}),
			},
			expected: true,
		},
		{
			name: "httpFallback wrapping TracingTransport without mTLS",
			transport: &httpFallback{
				super: tracing.NewTransport(&http.Transport{}),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := transportHasMTLS(tt.transport)
			require.Equal(t, tt.expected, result)
		})
	}
}
