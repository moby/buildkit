package gitutil

import (
	"reflect"
	"testing"
)

func TestParseGitRef(t *testing.T) {
	cases := map[string]*GitRef{
		"https://example.com/":    nil,
		"https://example.com/foo": nil,
		"https://example.com/foo.git": {
			Remote:    "https://example.com/foo.git",
			ShortName: "foo",
		},
		"https://example.com/foo.git#deadbeef": {
			Remote:    "https://example.com/foo.git",
			ShortName: "foo",
			Commit:    "deadbeef",
		},
		"https://example.com/foo.git#release/1.2": {
			Remote:    "https://example.com/foo.git",
			ShortName: "foo",
			Commit:    "release/1.2",
		},
		"https://example.com/foo.git/":    nil,
		"https://example.com/foo.git.bar": nil,
		"git://example.com/foo": {
			Remote:         "git://example.com/foo",
			ShortName:      "foo",
			UnencryptedTCP: true,
		},
		"github.com/moby/buildkit": {
			Remote: "github.com/moby/buildkit", ShortName: "buildkit",
			IndistinguishableFromLocal: true,
		},
		"https://github.com/moby/buildkit": nil,
		"https://github.com/moby/buildkit.git": {
			Remote:    "https://github.com/moby/buildkit.git",
			ShortName: "buildkit",
		},
		"git@github.com:moby/buildkit": {
			Remote:    "git@github.com:moby/buildkit",
			ShortName: "buildkit",
		},
		"git@github.com:moby/buildkit.git": {
			Remote:    "git@github.com:moby/buildkit.git",
			ShortName: "buildkit",
		},
		"git@bitbucket.org:atlassianlabs/atlassian-docker.git": {
			Remote:    "git@bitbucket.org:atlassianlabs/atlassian-docker.git",
			ShortName: "atlassian-docker",
		},
		"https://github.com/foo/bar.git#baz/qux:quux/quuz": {
			Remote:    "https://github.com/foo/bar.git",
			ShortName: "bar",
			Commit:    "baz/qux",
			SubDir:    "quux/quuz",
		},
		"http://github.com/docker/docker.git:#branch": nil,
	}
	for ref, expected := range cases {
		got, err := ParseGitRef(ref)
		if expected == nil {
			if err == nil {
				t.Errorf("expected an error for ParseGitRef(%q)", ref)
			}
		} else {
			if err != nil {
				t.Errorf("got an unexpected error: ParseGitRef(%q): %v", ref, err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Errorf("expected ParseGitRef(%q) to return %#v, got %#v", ref, expected, got)
			}
		}
	}
}
