package llb

import (
	"context"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestGit(t *testing.T) {
	t.Parallel()

	type tcase struct {
		name       string
		st         State
		identifier string
		attrs      map[string]string
	}

	tcases := []tcase{
		{
			name:       "refarg",
			st:         Git("github.com/foo/bar.git", "ref"),
			identifier: "git://github.com/foo/bar.git#ref",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
			},
		},
		{
			name:       "refarg with subdir",
			st:         Git("github.com/foo/bar.git", "ref:subdir"),
			identifier: "git://github.com/foo/bar.git#ref:subdir",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
			},
		},
		{
			name:       "refarg with subdir func",
			st:         Git("github.com/foo/bar.git", "ref", GitSubDir("subdir")),
			identifier: "git://github.com/foo/bar.git#ref:subdir",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
			},
		},
		{
			name:       "refarg with override",
			st:         Git("github.com/foo/bar.git", "ref:dir", GitRef("v1.0")),
			identifier: "git://github.com/foo/bar.git#v1.0:dir",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
			},
		},
		{
			name:       "funcs only",
			st:         Git("github.com/foo/bar.git", "", GitRef("v1.0"), GitSubDir("dir")),
			identifier: "git://github.com/foo/bar.git#v1.0:dir",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
			},
		},
		{
			name: "bundle",
			st: Git(
				"github.com/foo/bar.git", "",
				GitChecksum("1111111111111111111111111111111111111111"),
				GitBundleURL("docker-image+blob://example.com/repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			),
			identifier: "git://github.com/foo/bar.git",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
				"git.checksum":         "1111111111111111111111111111111111111111",
				"git.bundle":           "docker-image+blob://example.com/repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
		{
			name: "bundle oci store",
			st: Git(
				"github.com/foo/bar.git", "",
				GitChecksum("1111111111111111111111111111111111111111"),
				GitBundleURL(
					"oci-layout+blob://notreal/repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					GitBundleOCIStore("sess1", "store1"),
				),
			),
			identifier: "git://github.com/foo/bar.git",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
				"git.checksum":         "1111111111111111111111111111111111111111",
				"git.bundle":           "oci-layout+blob://notreal/repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"oci.session":          "sess1",
				"oci.store":            "store1",
			},
		},
		{
			name:       "checkout bundle",
			st:         Git("github.com/foo/bar.git", "", GitCheckoutBundle()),
			identifier: "git://github.com/foo/bar.git",
			attrs: map[string]string{
				"git.authheadersecret": "GIT_AUTH_HEADER",
				"git.authtokensecret":  "GIT_AUTH_TOKEN",
				"git.fullurl":          "https://github.com/foo/bar.git",
				"git.checkoutbundle":   "true",
			},
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st
			def, err := st.Marshal(context.TODO())

			require.NoError(t, err)

			m, arr := parseDef(t, def.Def)
			require.Equal(t, 2, len(arr))

			dgst, idx := last(t, arr)
			require.Equal(t, 0, idx)
			require.Equal(t, m[dgst], arr[0])

			g := arr[0].Op.(*pb.Op_Source).Source

			require.Equal(t, tc.identifier, g.Identifier)
			require.Equal(t, tc.attrs, g.Attrs)
		})
	}
}
