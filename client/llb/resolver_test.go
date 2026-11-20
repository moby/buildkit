package llb

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestImageResolveModeNever(t *testing.T) {
	def, err := Image("alpine", ResolveModeNever).Marshal(t.Context())
	require.NoError(t, err)
	op := &pb.Op{}
	require.NoError(t, op.UnmarshalVT(def.Def[0]))
	require.Equal(t, pb.AttrImageResolveModeNever, op.GetSource().Attrs[pb.AttrImageResolveMode])
	md := def.Metadata[digest.FromBytes(def.Def[0])]
	require.True(t, md.Caps[pb.CapSourceImageResolveModeNever])

	caps := pb.Caps.All()
	for _, cap := range caps {
		if cap.ID == string(pb.CapSourceImageResolveModeNever) {
			cap.Enabled = false
		}
	}
	unsupported := pb.Caps.CapSet(caps)
	err = unsupported.Supports(pb.CapSourceImageResolveModeNever)
	require.ErrorContains(t, err, string(pb.CapSourceImageResolveModeNever))
}

func TestImageMetaResolver(t *testing.T) {
	t.Parallel()
	tr := &testResolver{
		digest: digest.FromBytes([]byte("foo")),
		dir:    "/bar",
	}
	st := Image("alpine", WithMetaResolver(tr))

	require.Equal(t, false, tr.called)

	def, err := st.Marshal(t.Context(), LinuxPpc64le)
	require.NoError(t, err)

	require.Equal(t, true, tr.called)
	require.Equal(t, "linux/ppc64le", tr.platform)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 2, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[0])

	require.Equal(t, "docker-image://docker.io/library/alpine:latest", arr[0].Op.(*pb.Op_Source).Source.GetIdentifier())

	d, err := st.GetDir(t.Context())
	require.NoError(t, err)
	require.Equal(t, "/bar", d)
}

func TestImageResolveDigest(t *testing.T) {
	t.Parallel()

	st := Image("alpine", WithMetaResolver(&testResolver{
		digest: digest.FromBytes([]byte("bar")),
		dir:    "/foo",
	}), ResolveDigest(true))

	def, err := st.Marshal(t.Context())
	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 2, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[0])

	require.Equal(t, "docker-image://docker.io/library/alpine:latest@"+string(digest.FromBytes([]byte("bar"))), arr[0].Op.(*pb.Op_Source).Source.GetIdentifier())

	d, err := st.GetDir(t.Context())
	require.NoError(t, err)
	require.Equal(t, "/foo", d)
}

type testResolver struct {
	digest   digest.Digest
	dir      string
	called   bool
	platform string
}

func (r *testResolver) ResolveImageConfig(ctx context.Context, ref string, opt sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	var img struct {
		Config struct {
			Env        []string `json:"Env,omitempty"`
			WorkingDir string   `json:"WorkingDir,omitempty"`
			User       string   `json:"User,omitempty"`
		} `json:"config"`
	}
	r.called = true

	img.Config.WorkingDir = r.dir

	if imgOpt := opt.ImageOpt; imgOpt != nil && imgOpt.Platform != nil {
		r.platform = platforms.Format(*imgOpt.Platform)
	}

	dt, err := json.Marshal(img)
	if err != nil {
		return "", "", nil, errors.WithStack(err)
	}
	return ref, r.digest, dt, nil
}
