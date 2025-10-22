package gitobject

import (
	"crypto/sha1" //nolint:gosec // used for git object hashes
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseGitObject(t *testing.T) {
	rawCommit := `tree c60378fad086260463884449208b0545cef5703d
parent 2777c1b86e59a951188b81e310d3dc8b05b6ac9c
parent 916074cfc59ff9d4a29e0e1e74c3dd745b5e0d23
author Tõnis Tiigi <tonistiigi@gmail.com> 1758656883 -0700
committer GitHub <noreply@github.com> 1758656883 -0700
gpgsig -----BEGIN PGP SIGNATURE-----
 
 wsFcBAABCAAQBQJo0vlzCRC1aQ7uu5UhlAAAmL0QACwN0cWbwqWeoI14uhYSFqV2
 iRaUm2+vAoJcDkx4eRiKWyZxnCt1NApBpHZ3/R46nQyYf5syPAWtSfe/I9cBO6ed
 qiQldlYqaEDzKqLc5AKqmf+jP6PxvfOY7qX1Qnwe9IVNqVmrCak3GJchedQStxIz
 yHfx/4EFRf46LkpmmRgnou5P9Z6/em5zr70G8nOEGE0evSdgHF5hVkNzmfLBbRbI
 RkhvHYZJGdn+bphAFM487+ySbzEwz0sFxXiUn+sLkKUU/Y62jqto7WXrToXOoeCc
 h1DDrfSMzOJThQ+glgfyugXtBobXbuV62qLcnWfBoazWX6vc82UMlTH+lDd0rvQJ
 4Rx6jzeivwtIkMzOKDpJfEIaqXTLXEj5utI95t7vO9v6V/UctjLIr4K86o02VF7v
 6H60+3nKYt7kVeI0x6Hc3R9fWtohYAc8a/2xzyGxluqJokdaKOx5eTOrkAN/wdiv
 es/nyYwMW07fxAns0BoH3aHgwFrMWmGLovhwLOAdBgVYAuW2I5KNa6FavBUBaGWD
 /Cb2f8ZcaGVcvnwFEFkjcp9k+xCB2SGOJszbdehz86f/+rhQHJKvyIXvlWTYCswq
 K6/YilEke++bcpfoOV2PgOEmV4rWescIcxlremnYqtahR81BQYkrZYnihxIaVZc/
 Mq9vT7yP08mV6474Ihno
 =sixl
 -----END PGP SIGNATURE-----
 

Merge pull request #6237 from jsternberg/hack-compose

hack: update hack/compose with newer otel collector`
	shaCommit := `c8fad61a079f8272be5559fe473171a8f6333679`

	obj, err := Parse([]byte(rawCommit))
	require.NoError(t, err)

	sum, err := obj.Checksum(sha1.New)
	require.NoError(t, err)
	require.Equal(t, shaCommit, hex.EncodeToString(sum))
	err = obj.VerifyChecksum(shaCommit)
	require.NoError(t, err)

	err = obj.VerifyChecksum("0000000000000000000000000000000000000000")
	require.Error(t, err)

	const expectedSig = `-----BEGIN PGP SIGNATURE-----

wsFcBAABCAAQBQJo0vlzCRC1aQ7uu5UhlAAAmL0QACwN0cWbwqWeoI14uhYSFqV2
iRaUm2+vAoJcDkx4eRiKWyZxnCt1NApBpHZ3/R46nQyYf5syPAWtSfe/I9cBO6ed
qiQldlYqaEDzKqLc5AKqmf+jP6PxvfOY7qX1Qnwe9IVNqVmrCak3GJchedQStxIz
yHfx/4EFRf46LkpmmRgnou5P9Z6/em5zr70G8nOEGE0evSdgHF5hVkNzmfLBbRbI
RkhvHYZJGdn+bphAFM487+ySbzEwz0sFxXiUn+sLkKUU/Y62jqto7WXrToXOoeCc
h1DDrfSMzOJThQ+glgfyugXtBobXbuV62qLcnWfBoazWX6vc82UMlTH+lDd0rvQJ
4Rx6jzeivwtIkMzOKDpJfEIaqXTLXEj5utI95t7vO9v6V/UctjLIr4K86o02VF7v
6H60+3nKYt7kVeI0x6Hc3R9fWtohYAc8a/2xzyGxluqJokdaKOx5eTOrkAN/wdiv
es/nyYwMW07fxAns0BoH3aHgwFrMWmGLovhwLOAdBgVYAuW2I5KNa6FavBUBaGWD
/Cb2f8ZcaGVcvnwFEFkjcp9k+xCB2SGOJszbdehz86f/+rhQHJKvyIXvlWTYCswq
K6/YilEke++bcpfoOV2PgOEmV4rWescIcxlremnYqtahR81BQYkrZYnihxIaVZc/
Mq9vT7yP08mV6474Ihno
=sixl
-----END PGP SIGNATURE-----
`
	require.Equal(t, expectedSig, obj.GPGSig)

	const expectedSignedData = `tree c60378fad086260463884449208b0545cef5703d
parent 2777c1b86e59a951188b81e310d3dc8b05b6ac9c
parent 916074cfc59ff9d4a29e0e1e74c3dd745b5e0d23
author Tõnis Tiigi <tonistiigi@gmail.com> 1758656883 -0700
committer GitHub <noreply@github.com> 1758656883 -0700

Merge pull request #6237 from jsternberg/hack-compose

hack: update hack/compose with newer otel collector`
	require.Equal(t, expectedSignedData, obj.SignedData)

	require.Equal(t, []string{"c60378fad086260463884449208b0545cef5703d"}, obj.Headers["tree"])

	require.Equal(t, "commit", obj.Type)
	require.Equal(t, `Merge pull request #6237 from jsternberg/hack-compose

hack: update hack/compose with newer otel collector`, obj.Message)

	commit, err := obj.ToCommit()
	require.NoError(t, err)
	require.Equal(t, "Tõnis Tiigi", commit.Author.Name)
	require.Equal(t, "tonistiigi@gmail.com", commit.Author.Email)
	require.Equal(t, int64(1758656883), commit.Author.When.Unix())
	_, offset := commit.Author.When.Zone()
	require.Equal(t, -7*3600, offset)

	require.Equal(t, 2, len(commit.Parents))
	require.Equal(t, "2777c1b86e59a951188b81e310d3dc8b05b6ac9c", commit.Parents[0])
	require.Equal(t, "916074cfc59ff9d4a29e0e1e74c3dd745b5e0d23", commit.Parents[1])

	require.Equal(t, "GitHub", commit.Committer.Name)
	require.Equal(t, "noreply@github.com", commit.Committer.Email)
	require.Equal(t, int64(1758656883), commit.Committer.When.Unix())
	_, offset = commit.Committer.When.Zone()
	require.Equal(t, -7*3600, offset)

	_, err = obj.ToTag()
	require.Error(t, err)

	shaTag := `813f42e05f8d94edb13c70a51dc6f8457c08a672`
	raw := `object c8fad61a079f8272be5559fe473171a8f6333679
type commit
tag v0.25.0-rc1
tagger Tonis Tiigi <tonistiigi@gmail.com> 1758658255 -0700

v0.25.0-rc1
-----BEGIN PGP SIGNATURE-----

iQGzBAABCAAdFiEEVxbwWSJT3TMaEhiGr6neX4q3rzkFAmjS/s8ACgkQr6neX4q3
rzlE+Av/b2FbO2AoBf0CQrRK/ggRcz1lla4xEyV+VrU3M/oEo1kKfpyQE4qXPSZC
ybn7busZbmwrGpwXbMYk6ADg2lANhR4sjwWPw4b4hh3n0iSlD37eu63UEUeoIHCh
+Low+l5gkojExY8OfG8t34FZMdLzuA70lVclZHYEh+ucDb9kg6GlJCPQ3i1CizPj
iCkRr2veuTNDykSdS9aqI3NJ5EXJT7A9CFZ7dGTilH+6o4PJLSIp0xl8AWecLju2
0VYQtfo3W0UftxRLldU+KTc2UqUI3A5I/KyryYd8pJnTkh9xnO8TvF9O/wkvNUem
pzY8yP5t3/wYbnjrCYhpEauXc/JCxh6dh/mTvcs2GoyLBYRSAtOei10UutdNDf5t
xBB+AG8ocd4ro+kxXhabvSWG3kmLXkaBXVG31LLcIGEfVNP9hnzV0DXbLCzVBD7+
SxdXrB7XhARUFFIKtobmBmLfAww18R6DGD0pNE+uSLplUy2D5HJA2R4UnJb+0DIR
yVkI8y/p
=12Xx
-----END PGP SIGNATURE-----
`
	obj, err = Parse([]byte(raw))
	require.NoError(t, err)

	require.Equal(t, "tag", obj.Type)
	require.Equal(t, []string{"v0.25.0-rc1"}, obj.Headers["tag"])
	require.Equal(t, "v0.25.0-rc1", obj.Message)

	const expectedTagSig = `-----BEGIN PGP SIGNATURE-----

iQGzBAABCAAdFiEEVxbwWSJT3TMaEhiGr6neX4q3rzkFAmjS/s8ACgkQr6neX4q3
rzlE+Av/b2FbO2AoBf0CQrRK/ggRcz1lla4xEyV+VrU3M/oEo1kKfpyQE4qXPSZC
ybn7busZbmwrGpwXbMYk6ADg2lANhR4sjwWPw4b4hh3n0iSlD37eu63UEUeoIHCh
+Low+l5gkojExY8OfG8t34FZMdLzuA70lVclZHYEh+ucDb9kg6GlJCPQ3i1CizPj
iCkRr2veuTNDykSdS9aqI3NJ5EXJT7A9CFZ7dGTilH+6o4PJLSIp0xl8AWecLju2
0VYQtfo3W0UftxRLldU+KTc2UqUI3A5I/KyryYd8pJnTkh9xnO8TvF9O/wkvNUem
pzY8yP5t3/wYbnjrCYhpEauXc/JCxh6dh/mTvcs2GoyLBYRSAtOei10UutdNDf5t
xBB+AG8ocd4ro+kxXhabvSWG3kmLXkaBXVG31LLcIGEfVNP9hnzV0DXbLCzVBD7+
SxdXrB7XhARUFFIKtobmBmLfAww18R6DGD0pNE+uSLplUy2D5HJA2R4UnJb+0DIR
yVkI8y/p
=12Xx
-----END PGP SIGNATURE-----
`
	require.Equal(t, expectedTagSig, obj.GPGSig)

	const expectedTagSignedData = `object c8fad61a079f8272be5559fe473171a8f6333679
type commit
tag v0.25.0-rc1
tagger Tonis Tiigi <tonistiigi@gmail.com> 1758658255 -0700

v0.25.0-rc1`
	require.Equal(t, expectedTagSignedData, obj.SignedData)

	sum, err = obj.Checksum(sha1.New)
	require.NoError(t, err)
	require.Equal(t, shaTag, hex.EncodeToString(sum))

	err = obj.VerifyChecksum(shaTag)
	require.NoError(t, err)

	tag, err := obj.ToTag()
	require.NoError(t, err)
	require.Equal(t, "v0.25.0-rc1", tag.Tag)
	require.Equal(t, "c8fad61a079f8272be5559fe473171a8f6333679", tag.Object)
	require.Equal(t, "commit", tag.Type)
	require.Equal(t, "Tonis Tiigi", tag.Tagger.Name)
	require.Equal(t, "tonistiigi@gmail.com", tag.Tagger.Email)
	require.Equal(t, int64(1758658255), tag.Tagger.When.Unix())
	_, offset = tag.Tagger.When.Zone()
	require.Equal(t, -7*3600, offset)
}

func TestParseActor(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string
		wantMail string
		wantTime *time.Time
	}{
		{
			name:     "normal",
			input:    "User One <user1@example.com> 1759247759 -0500",
			wantName: "User One",
			wantMail: "user1@example.com",
			wantTime: func() *time.Time {
				loc := time.FixedZone("", -5*3600)
				t := time.Unix(1759247759, 0).In(loc)
				return &t
			}(),
		},
		{
			name:     "no timestamp",
			input:    "User Two <user2@example.com>",
			wantName: "User Two",
			wantMail: "user2@example.com",
			wantTime: nil,
		},
		{
			name:     "invalid timestamp",
			input:    "User Three <user3@example.com> notatime +0000",
			wantName: "User Three",
			wantMail: "user3@example.com",
			wantTime: nil,
		},
		{
			name:     "invalid tz format",
			input:    "User Four <user4@example.com> 1759247759 500",
			wantName: "User Four",
			wantMail: "user4@example.com",
			wantTime: nil,
		},
		{
			name:     "missing angle brackets",
			input:    "User Five user5@example.com 1759247759 +0000",
			wantName: "User Five user5@example.com 1759247759 +0000",
			wantMail: "",
			wantTime: nil,
		},
		{
			name:     "extra spaces",
			input:    "   User Six   <user6@example.com>   1600000000 +0200   ",
			wantName: "User Six",
			wantMail: "user6@example.com",
			wantTime: func() *time.Time {
				loc := time.FixedZone("", 2*3600)
				t := time.Unix(1600000000, 0).In(loc)
				return &t
			}(),
		},
		{
			name:     "name contains angle bracket",
			input:    "Strange <Name <strange@example.com> 1600000000 +0000",
			wantName: "Strange <Name",
			wantMail: "strange@example.com",
			wantTime: func() *time.Time {
				loc := time.FixedZone("", 0)
				t := time.Unix(1600000000, 0).In(loc)
				return &t
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseActor(tt.input)

			require.Equal(t, tt.wantName, got.Name)
			require.Equal(t, tt.wantMail, got.Email)

			if tt.wantTime == nil {
				require.Nil(t, got.When)
			} else {
				require.NotNil(t, got.When)
				require.Equal(t, tt.wantTime.Unix(), got.When.Unix())
				// compare timezone by offset (Zone), not by Location string which can be empty
				_, wantOffset := tt.wantTime.Zone()
				_, gotOffset := got.When.Zone()
				require.Equal(t, wantOffset, gotOffset)
			}
		})
	}
}
