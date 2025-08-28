package dfgitutil

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseGitRef(t *testing.T) {
	cases := []struct {
		ref      string
		expected *GitRef
		err      string
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
				Ref:       "deadbeef",
			},
		},
		{
			ref: "https://example.com/foo.git#release/1.2",
			expected: &GitRef{
				Remote:    "https://example.com/foo.git",
				ShortName: "foo",
				Ref:       "release/1.2",
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
			ref: "github.com/moby/buildkit#master",
			expected: &GitRef{
				Remote:                     "github.com/moby/buildkit",
				ShortName:                  "buildkit",
				IndistinguishableFromLocal: true,
				Ref:                        "master",
			},
		},
		{
			ref:      "custom.xyz/moby/buildkit.git",
			expected: nil,
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
			ref: "https://foo:bar@github.com/moby/buildkit.git",
			expected: &GitRef{
				Remote:    "https://foo:bar@github.com/moby/buildkit.git",
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
				Ref:       "baz/qux",
				SubDir:    "quux/quuz",
			},
		},
		{
			ref:      "http://github.com/docker/docker.git:#branch",
			expected: nil,
		},
		{
			ref: "https://github.com/docker/docker.git#:myfolder",
			expected: &GitRef{
				Remote:    "https://github.com/docker/docker.git",
				ShortName: "docker",
				SubDir:    "myfolder",
			},
		},
		{
			ref:      "./.git",
			expected: nil,
		},
		{
			ref:      ".git",
			expected: nil,
		},
		{
			ref: "https://github.com/docker/docker.git?ref=v1.0.0&subdir=/subdir",
			expected: &GitRef{
				Remote:    "https://github.com/docker/docker.git",
				ShortName: "docker",
				Ref:       "v1.0.0",
				SubDir:    "/subdir",
			},
		},
		{
			ref: "https://github.com/moby/buildkit.git?subdir=/subdir#v1.0.0",
			expected: &GitRef{
				Remote:    "https://github.com/moby/buildkit.git",
				ShortName: "buildkit",
				Ref:       "v1.0.0",
				SubDir:    "/subdir",
			},
		},
		{
			ref: "https://github.com/moby/buildkit.git?tag=v1.0.0",
			expected: &GitRef{
				Remote:    "https://github.com/moby/buildkit.git",
				ShortName: "buildkit",
				Ref:       "refs/tags/v1.0.0",
			},
		},
		{
			ref: "github.com/moby/buildkit?tag=v1.0.0",
			expected: &GitRef{
				Remote:                     "github.com/moby/buildkit",
				ShortName:                  "buildkit",
				Ref:                        "refs/tags/v1.0.0",
				IndistinguishableFromLocal: true,
			},
		},
		{
			ref: "https://github.com/moby/buildkit.git?branch=v1.0",
			expected: &GitRef{
				Remote:    "https://github.com/moby/buildkit.git",
				ShortName: "buildkit",
				Ref:       "refs/heads/v1.0",
			},
		},
		{
			ref: "https://github.com/moby/buildkit.git?ref=v1.0.0#v1.2.3",
			err: "ref conflicts",
		},
		{
			ref: "https://github.com/moby/buildkit.git?ref=v1.0.0&tag=v1.2.3",
			err: "ref conflicts",
		},
		{
			// TODO: consider allowing this, when the tag actually exists on the branch
			ref: "https://github.com/moby/buildkit.git?tag=v1.0.0&branch=v1.0",
			err: "branch conflicts with tag",
		},
		{
			ref: "git@github.com:moby/buildkit.git?subdir=/subdir#v1.0.0",
			expected: &GitRef{
				Remote:    "git@github.com:moby/buildkit.git",
				ShortName: "buildkit",
				Ref:       "v1.0.0",
				SubDir:    "/subdir",
			},
		},
		{
			ref: "https://github.com/moby/buildkit.git?invalid=123",
			err: "unexpected query \"invalid\"",
		},
	}
	for i, tt := range cases {
		t.Run(fmt.Sprintf("case%d", i+1), func(t *testing.T) {
			got, _, err := ParseGitRef(tt.ref)
			if tt.expected == nil {
				require.Nil(t, got)
				require.Error(t, err)
				if tt.err != "" {
					require.ErrorContains(t, err, tt.err)
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expected, got)
			}
		})
	}
}
