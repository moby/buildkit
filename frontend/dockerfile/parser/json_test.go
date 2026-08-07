package parser

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

var invalidJSONArraysOfStrings = []string{
	`["a",42,"b"]`,
	`["a",123.456,"b"]`,
	`["a",{},"b"]`,
	`["a",{"c": "d"},"b"]`,
	`["a",["c"],"b"]`,
	`["a",true,"b"]`,
	`["a",false,"b"]`,
	`["a",null,"b"]`,
}

var validJSONArraysOfStrings = map[string][]string{
	`[]`:             {},
	`[""]`:           {""},
	`["a"]`:          {"a"},
	`["a","b"]`:      {"a", "b"},
	`[ "a", "b" ]`:   {"a", "b"},
	`[	"a",	"b"	]`:   {"a", "b"},
	`	[	"a",	"b"	]	`: {"a", "b"},
	`["abc 123", "♥", "☃", "\" \\ \/ \b \f \n \r \t \u0000"]`: {"abc 123", "♥", "☃", "\" \\ / \b \f \n \r \t \u0000"},
}

func TestJSONArraysOfStrings(t *testing.T) {
	for json, expected := range validJSONArraysOfStrings {
		node, _, err := parseJSON(json)
		require.NoErrorf(t, err, "%q should be a valid JSON array of strings, but wasn't! (err: %q)", json, err)
		i := 0
		for node != nil {
			require.Lessf(t, i, len(expected), "expected result is shorter than parsed result (%d vs %d+) in %q", len(expected), i+1, json)
			require.Equalf(t, node.Value, expected[i], "expected %q (not %q) in %q at pos %d", expected[i], node.Value, json, i)
			node = node.Next
			i++
		}
		require.Equalf(t, len(expected), i, "expected result is longer than parsed result (%d vs %d) in %q", len(expected), i+1, json)
	}
	for _, json := range invalidJSONArraysOfStrings {
		_, _, err := parseJSON(json)
		require.Truef(t, errors.Is(err, errDockerfileNotStringArray), "%q should be an invalid JSON array of strings, but wasn't!", json)
	}
}
