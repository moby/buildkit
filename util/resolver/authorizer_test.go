package resolver

import (
	"net/http"
	"reflect"
	"testing"
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

func TestIsCrossRepoBlobMountRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		url      string
		method   string
		expected bool
	}{
		{
			name:     "IsMount",
			url:      "https://registry.io/v2/repo/image/blobs/uploads/?mount=6363fe744f74ee8f280958ab2f185dde&from=anotherrepo",
			method:   http.MethodPost,
			expected: true,
		},
		{
			name:     "NotPost",
			url:      "https://registry.io/v2/repo/image/blobs/uploads/?mount=6363fe744f74ee8f280958ab2f185dde&from=anotherrepo",
			method:   http.MethodGet,
			expected: false,
		},
		{
			name:     "NoFrom",
			url:      "https://registry.io/v2/repo/image/blobs/uploads/?mount=6363fe744f74ee8f280958ab2f185dde",
			method:   http.MethodPost,
			expected: false,
		},
		{
			name:     "NoMount",
			url:      "https://registry.io/v2/repo/image/blobs/uploads/?from=anotherrepo",
			method:   http.MethodGet,
			expected: false,
		},
		{
			name:     "WrongSuffix",
			url:      "https://registry.io/v2/repo/image/uploads/?mount=6363fe744f74ee8f280958ab2f185dde&from=anotherrepo",
			method:   http.MethodGet,
			expected: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatalf("error creating test request %v", err)
			}
			actual := isCrossRepoBlobMountRequest(req)
			if actual != tc.expected {
				t.Fatalf("expected %t, got %t", tc.expected, actual)
			}
		})
	}
}
