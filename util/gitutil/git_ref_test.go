package gitutil

import (
	"reflect"
	"testing"
)

func TestParseGitRef(t *testing.T) {
	cases := []struct {
		ref      string
		expected *GitRef
	}{
		{
			ref:      "https://example.com/",
			expected: nil,
		},
		{
			ref:      "https://example.com/foo",
			expected: nil,
		},
		{
			ref: "https://example.com/foo.git",
			expected: &GitRef{
				Remote:    "https://example.com/foo.git",
				ShortName: "foo",
			},
		},
		{
			ref: "https://example.com/foo.git#deadbeef",
			expected: &GitRef{
				Remote:    "https://example.com/foo.git",
				ShortName: "foo",
				Commit:    "deadbeef",
			},
		},
		{
			ref: "https://example.com/foo.git#release/1.2",
			expected: &GitRef{
				Remote:    "https://example.com/foo.git",
				ShortName: "foo",
				Commit:    "release/1.2",
			},
		},
		{
			ref:      "https://example.com/foo.git/",
			expected: nil,
		},
		{
			ref:      "https://example.com/foo.git.bar",
			expected: nil,
		},
		{
			ref: "git://example.com/foo",
			expected: &GitRef{
				Remote:         "git://example.com/foo",
				ShortName:      "foo",
				UnencryptedTCP: true,
			},
		},
		{
			ref: "github.com/moby/buildkit",
			expected: &GitRef{
				Remote:                     "github.com/moby/buildkit",
				ShortName:                  "buildkit",
				IndistinguishableFromLocal: true,
			},
		},
		{
			ref:      "https://github.com/moby/buildkit",
			expected: nil,
		},
		{
			ref: "https://github.com/moby/buildkit.git",
			expected: &GitRef{
				Remote:    "https://github.com/moby/buildkit.git",
				ShortName: "buildkit",
			},
		},
		{
			ref: "git@github.com:moby/buildkit",
			expected: &GitRef{
				Remote:    "git@github.com:moby/buildkit",
				ShortName: "buildkit",
			},
		},
		{
			ref: "git@github.com:moby/buildkit.git",
			expected: &GitRef{
				Remote:    "git@github.com:moby/buildkit.git",
				ShortName: "buildkit",
			},
		},
		{
			ref: "git@bitbucket.org:atlassianlabs/atlassian-docker.git",
			expected: &GitRef{
				Remote:    "git@bitbucket.org:atlassianlabs/atlassian-docker.git",
				ShortName: "atlassian-docker",
			},
		},
		{
			ref: "https://github.com/foo/bar.git#baz/qux:quux/quuz",
			expected: &GitRef{
				Remote:    "https://github.com/foo/bar.git",
				ShortName: "bar",
				Commit:    "baz/qux",
				SubDir:    "quux/quuz",
			},
		},
		{
			ref:      "http://github.com/docker/docker.git:#branch",
			expected: nil,
		},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.ref, func(t *testing.T) {
			got, err := ParseGitRef(tt.ref)
			if tt.expected == nil {
				if err == nil {
					t.Errorf("expected an error for ParseGitRef(%q)", tt.ref)
				}
			} else {
				if err != nil {
					t.Errorf("got an unexpected error: ParseGitRef(%q): %v", tt.ref, err)
				}
				if !reflect.DeepEqual(got, tt.expected) {
					t.Errorf("expected ParseGitRef(%q) to return %#v, got %#v", tt.ref, tt.expected, got)
				}
			}
		})
	}
}
