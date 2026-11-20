//go:build dfmtime

package dockerfile2llb

import (
	"testing"
	"time"

	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/appcontext"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func linuxConvertOpt() ConvertOpt {
	linuxPlatform := ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	return ConvertOpt{
		Config: dockerui.Config{
			TargetPlatforms: []ocispecs.Platform{linuxPlatform},
		},
		TargetPlatform: &linuxPlatform,
	}
}

func collectFileActions(t *testing.T, df string, opt ConvertOpt) []*pb.FileAction {
	t.Helper()
	res, err := Dockerfile2LLB(appcontext.Context(), []byte(df), opt)
	require.NoError(t, err)
	require.NotNil(t, res.Image)
	def, err := res.State.Marshal(appcontext.Context())
	require.NoError(t, err)

	var out []*pb.FileAction
	for _, dt := range def.Def {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(dt))
		if fop := op.GetFile(); fop != nil {
			out = append(out, fop.Actions...)
		}
	}
	return out
}

func TestCopyMtime(t *testing.T) {
	t.Parallel()

	t.Run("unix seconds via SOURCE_DATE_EPOCH", func(t *testing.T) {
		t.Parallel()
		df := "ARG SOURCE_DATE_EPOCH=1704067200\nFROM scratch\nARG SOURCE_DATE_EPOCH\nCOPY --mtime=$SOURCE_DATE_EPOCH foo /bar\n"
		actions := collectFileActions(t, df, linuxConvertOpt())
		require.Len(t, actions, 1)
		cp := actions[0].GetCopy()
		require.NotNil(t, cp)
		require.Equal(t, time.Unix(1704067200, 0).UnixNano(), cp.Timestamp)
	})

	t.Run("at-prefixed epoch and RFC3339", func(t *testing.T) {
		t.Parallel()
		df := "FROM scratch\nCOPY --mtime=@42 a /b\nCOPY --mtime=2024-01-01T00:00:00.5Z c /d\n"
		actions := collectFileActions(t, df, linuxConvertOpt())
		require.Len(t, actions, 2)
		require.Equal(t, time.Unix(42, 0).UnixNano(), actions[0].GetCopy().Timestamp)
		require.Equal(t, time.Date(2024, 1, 1, 0, 0, 0, 500_000_000, time.UTC).UnixNano(), actions[1].GetCopy().Timestamp)
	})

	t.Run("unset arg leaves timestamp unset", func(t *testing.T) {
		t.Parallel()
		df := "FROM scratch\nARG SOURCE_DATE_EPOCH\nCOPY --mtime=$SOURCE_DATE_EPOCH a /b\n"
		actions := collectFileActions(t, df, linuxConvertOpt())
		require.Len(t, actions, 1)
		require.Equal(t, int64(-1), actions[0].GetCopy().Timestamp)
	})

	t.Run("heredoc and --link", func(t *testing.T) {
		t.Parallel()
		caps := pb.Caps.CapSet(pb.Caps.All())
		opt := linuxConvertOpt()
		opt.LLBCaps = &caps
		df := "FROM scratch\nCOPY --link --mtime=0 <<EOF /hello.txt\nhi\nEOF\n"
		actions := collectFileActions(t, df, opt)
		require.Len(t, actions, 2)
		var sawMkfile, sawCopy bool
		for _, act := range actions {
			if mk := act.GetMkfile(); mk != nil {
				sawMkfile = true
				require.Equal(t, int64(0), mk.Timestamp)
			}
			if cp := act.GetCopy(); cp != nil {
				sawCopy = true
				require.Equal(t, int64(0), cp.Timestamp)
			}
		}
		require.True(t, sawMkfile)
		require.True(t, sawCopy)
	})

	t.Run("invalid mtime rejected", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			df   string
			want string
		}{
			{
				name: "unparseable",
				df:   "FROM scratch\nCOPY --mtime=not-a-time a /b\n",
				want: "invalid mtime parameter",
			},
			{
				name: "at-prefix without integer",
				df:   "FROM scratch\nCOPY --mtime=@2024-01-01T00:00:00Z a /b\n",
				want: "invalid mtime parameter",
			},
			{
				name: "out of unix nano upper bound",
				df:   "FROM scratch\nCOPY --mtime=9999-12-31T23:59:59Z a /b\n",
				want: "out of supported range",
			},
			{
				name: "out of unix nano lower bound",
				df:   "FROM scratch\nCOPY --mtime=0001-01-01T00:00:00Z a /b\n",
				want: "out of supported range",
			},
			{
				name: "overflowing epoch seconds",
				df:   "FROM scratch\nCOPY --mtime=9223372036854775807 a /b\n",
				want: "out of supported range",
			},
			{
				name: "reserved -1ns sentinel",
				df:   "FROM scratch\nCOPY --mtime=1969-12-31T23:59:59.999999999Z a /b\n",
				want: "-1ns before Unix epoch is reserved",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := Dockerfile2LLB(appcontext.Context(), []byte(tc.df), linuxConvertOpt())
				require.ErrorContains(t, err, tc.want)
			})
		}
	})

	t.Run("windows target rejected", func(t *testing.T) {
		t.Parallel()
		winPlatform := ocispecs.Platform{OS: "windows", Architecture: "amd64"}
		df := "FROM scratch\nCOPY --mtime=42 a /b\n"
		_, err := Dockerfile2LLB(appcontext.Context(), []byte(df), ConvertOpt{
			Config: dockerui.Config{
				TargetPlatforms: []ocispecs.Platform{winPlatform},
			},
			TargetPlatform: &winPlatform,
		})
		require.ErrorContains(t, err, "--mtime is not supported on Windows")
	})
}
