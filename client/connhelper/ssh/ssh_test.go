package ssh

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpecFromURL(t *testing.T) {
	cases := map[string]*Spec{
		"ssh://foo": {
			Host: "foo",
		},
		"ssh://me@foo:10022": {
			User: "me", Host: "foo", Port: "10022",
		},
		"ssh://me:passw0rd@foo": nil,
		"ssh://foo/bar":         nil,
		"ssh://foo?bar":         nil,
		"ssh://foo#bar":         nil,
		"ssh://":                nil,
	}
	for s, expected := range cases {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SpecFromURL(u)
		if expected != nil {
			require.NoError(t, err)
			require.EqualValues(t, expected, got, s)
		} else {
			require.Error(t, err, s)
		}
	}
}
