package git

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestNewGitIdentifier(t *testing.T) {
	tests := []struct {
		url      string
		expected GitIdentifier
	}{
		{
			url: "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git",
			expected: GitIdentifier{
				Remote: "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git",
			},
		},
		{
			url: "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git#main",
			expected: GitIdentifier{
				Remote: "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git",
				Ref:    "main",
			},
		},
		{
			url: "git@github.com:moby/buildkit.git",
			expected: GitIdentifier{
				Remote: "git@github.com:moby/buildkit.git",
			},
		},
		{
			url: "github.com/moby/buildkit.git#main",
			expected: GitIdentifier{
				Remote: "https://github.com/moby/buildkit.git",
				Ref:    "main",
			},
		},
		{
			url: "git://github.com/user/repo.git",
			expected: GitIdentifier{
				Remote: "git://github.com/user/repo.git",
			},
		},
		{
			url: "git://github.com/user/repo.git#mybranch:mydir/mysubdir/",
			expected: GitIdentifier{
				Remote: "git://github.com/user/repo.git",
				Ref:    "mybranch",
				Subdir: "mydir/mysubdir",
			},
		},
		{
			url: "git://github.com/user/repo.git#:mydir/mysubdir/",
			expected: GitIdentifier{
				Remote: "git://github.com/user/repo.git",
				Subdir: "mydir/mysubdir",
			},
		},
		{
			url: "https://github.com/user/repo.git",
			expected: GitIdentifier{
				Remote: "https://github.com/user/repo.git",
			},
		},
		{
			url: "https://github.com/user/repo.git#mybranch:mydir/mysubdir/",
			expected: GitIdentifier{
				Remote: "https://github.com/user/repo.git",
				Ref:    "mybranch",
				Subdir: "mydir/mysubdir",
			},
		},
		{
			url: "git@github.com:user/repo.git",
			expected: GitIdentifier{
				Remote: "git@github.com:user/repo.git",
			},
		},
		{
			url: "git@github.com:user/repo.git#mybranch:mydir/mysubdir/",
			expected: GitIdentifier{
				Remote: "git@github.com:user/repo.git",
				Ref:    "mybranch",
				Subdir: "mydir/mysubdir",
			},
		},
		{
			url: "ssh://github.com/user/repo.git",
			expected: GitIdentifier{
				Remote: "ssh://github.com/user/repo.git",
			},
		},
		{
			url: "ssh://github.com/user/repo.git#mybranch:mydir/mysubdir/",
			expected: GitIdentifier{
				Remote: "ssh://github.com/user/repo.git",
				Ref:    "mybranch",
				Subdir: "mydir/mysubdir",
			},
		},
		{
			url: "ssh://foo%40barcorp.com@github.com/user/repo.git#mybranch:mydir/mysubdir/",
			expected: GitIdentifier{
				Remote: "ssh://foo%40barcorp.com@github.com/user/repo.git",
				Ref:    "mybranch",
				Subdir: "mydir/mysubdir",
			},
		},
		{
			url: "https://github.com/user/repo.git#main:../../escape",
			expected: GitIdentifier{
				Remote: "https://github.com/user/repo.git",
				Ref:    "main",
				Subdir: "escape",
			},
		},
		{
			url: "https://github.com/user/repo.git#main:dir/../../escape",
			expected: GitIdentifier{
				Remote: "https://github.com/user/repo.git",
				Ref:    "main",
				Subdir: "escape",
			},
		},
		{
			url: "https://github.com/user/repo.git#main:/absolute/path",
			expected: GitIdentifier{
				Remote: "https://github.com/user/repo.git",
				Ref:    "main",
				Subdir: "absolute/path",
			},
		},
		{
			url: "https://github.com/user/repo.git#main:../",
			expected: GitIdentifier{
				Remote: "https://github.com/user/repo.git",
				Ref:    "main",
			},
		},
		{
			url: "ssh://github.com/user/repo.git#main:../../escape",
			expected: GitIdentifier{
				Remote: "ssh://github.com/user/repo.git",
				Ref:    "main",
				Subdir: "escape",
			},
		},
		{
			url: "ssh://github.com/user/repo.git#main:/absolute/path",
			expected: GitIdentifier{
				Remote: "ssh://github.com/user/repo.git",
				Ref:    "main",
				Subdir: "absolute/path",
			},
		},
		{
			url: "git@github.com:user/repo.git#main:../../escape",
			expected: GitIdentifier{
				Remote: "git@github.com:user/repo.git",
				Ref:    "main",
				Subdir: "escape",
			},
		},
		{
			url: "git@github.com:user/repo.git#main:/absolute/path",
			expected: GitIdentifier{
				Remote: "git@github.com:user/repo.git",
				Ref:    "main",
				Subdir: "absolute/path",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			gi, err := NewGitIdentifier(tt.url)
			require.NoError(t, err)
			require.Equal(t, tt.expected, *gi)
		})
	}
}

// TestIdentifierBundleValidation exercises the attribute-level validation
// performed by (*Source).Identifier when git.bundle / git.checkoutbundle attrs
// are present. It covers both positive (should parse cleanly) and negative
// (should reject with a specific message) cases.
func TestIdentifierBundleValidation(t *testing.T) {
	const validBundle = "docker-image+blob://example.com/repo/bundles@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const validChecksum = "1111111111111111111111111111111111111111"

	tests := []struct {
		name    string
		url     string
		attrs   map[string]string
		wantErr string
		assert  func(t *testing.T, id *GitIdentifier)
	}{
		{
			name: "valid bundle+checksum",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitBundle:   validBundle,
				pb.AttrGitChecksum: validChecksum,
			},
			assert: func(t *testing.T, id *GitIdentifier) {
				require.Equal(t, validBundle, id.Bundle)
				require.Equal(t, validChecksum, id.Checksum)
				require.False(t, id.CheckoutBundle)
			},
		},
		{
			name: "valid oci-layout bundle",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitBundle:   "oci-layout+blob://local/bundle@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				pb.AttrGitChecksum: validChecksum,
			},
			assert: func(t *testing.T, id *GitIdentifier) {
				require.Equal(t, "oci-layout+blob://local/bundle@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", id.Bundle)
			},
		},
		{
			name: "bundle without checksum",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitBundle: validBundle,
			},
			wantErr: "git.bundle requires git.checksum to be set",
		},
		{
			name: "bundle with https scheme",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitBundle:   "https://example.com/bundle",
				pb.AttrGitChecksum: validChecksum,
			},
			wantErr: `git.bundle locator scheme "https" is not supported`,
		},
		{
			name: "bundle with sha512 digest",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitBundle:   "docker-image+blob://example.com/repo/bundles@sha512:cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e",
				pb.AttrGitChecksum: validChecksum,
			},
			wantErr: `digest algorithm "sha512" not supported`,
		},
		{
			name: "checkoutbundle with keepgitdir",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitCheckoutBundle: "true",
				pb.AttrKeepGitDir:        "true",
			},
			wantErr: "git.checkoutbundle is incompatible with git.keepgitdir",
		},
		{
			name: "checkoutbundle with subdir",
			url:  "https://example.com/repo.git#main:sub",
			attrs: map[string]string{
				pb.AttrGitCheckoutBundle: "true",
			},
			wantErr: "git.checkoutbundle is incompatible with git.subdir",
		},
		{
			name: "checkoutbundle alone",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitCheckoutBundle: "true",
			},
			assert: func(t *testing.T, id *GitIdentifier) {
				require.True(t, id.CheckoutBundle)
				require.Empty(t, id.Bundle)
			},
		},
		{
			name: "bundle+checkoutbundle",
			url:  "https://example.com/repo.git",
			attrs: map[string]string{
				pb.AttrGitBundle:         validBundle,
				pb.AttrGitChecksum:       validChecksum,
				pb.AttrGitCheckoutBundle: "true",
			},
			assert: func(t *testing.T, id *GitIdentifier) {
				require.True(t, id.CheckoutBundle)
				require.Equal(t, validBundle, id.Bundle)
			},
		},
		{
			name: "bundle ref-sha mismatches checksum",
			url:  "https://example.com/repo.git#2222222222222222222222222222222222222222",
			attrs: map[string]string{
				pb.AttrGitBundle:   validBundle,
				pb.AttrGitChecksum: validChecksum,
			},
			wantErr: "expected checksum to match 1111111111111111111111111111111111111111",
		},
		{
			name: "bundle ref-sha matches checksum",
			url:  "https://example.com/repo.git#" + validChecksum,
			attrs: map[string]string{
				pb.AttrGitBundle:   validBundle,
				pb.AttrGitChecksum: validChecksum,
			},
			assert: func(t *testing.T, id *GitIdentifier) {
				require.Equal(t, validChecksum, id.Ref)
				require.Equal(t, validChecksum, id.Checksum)
			},
		},
	}

	src := &Source{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := src.Identifier("git", tt.url, tt.attrs, nil)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			gid, ok := id.(*GitIdentifier)
			require.True(t, ok)
			if tt.assert != nil {
				tt.assert(t, gid)
			}
		})
	}
}
