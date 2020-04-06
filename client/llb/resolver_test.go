package llb

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestImageMetaResolver(t *testing.T) {
	t.Parallel()
	tr := &testResolver{
		digest: digest.FromBytes([]byte("foo")),
		dir:    "/bar",
	}
	st := Image("alpine", WithMetaResolver(tr))

	require.Equal(t, false, tr.called)

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	require.Equal(t, true, tr.called)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 2, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[0])

	require.Equal(t, "docker-image://docker.io/library/alpine:latest", arr[0].Op.(*pb.Op_Source).Source.GetIdentifier())

	d, err := st.GetDir(context.TODO())
	require.NoError(t, err)
	require.Equal(t, "/bar", d)
}

func TestImageResolveDigest(t *testing.T) {
	t.Parallel()

	st := Image("alpine", WithMetaResolver(&testResolver{
		digest: digest.FromBytes([]byte("bar")),
		dir:    "/foo",
	}), ResolveDigest(true))

	def, err := st.Marshal(context.TODO())
	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 2, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[0])

	require.Equal(t, "docker-image://docker.io/library/alpine:latest@"+string(digest.FromBytes([]byte("bar"))), arr[0].Op.(*pb.Op_Source).Source.GetIdentifier())

	d, err := st.GetDir(context.TODO())
	require.NoError(t, err)
	require.Equal(t, "/foo", d)
}

type testResolver struct {
	digest digest.Digest
	dir    string
	called bool
}

func (r *testResolver) ResolveImageConfig(ctx context.Context, ref string, opt ResolveImageConfigOpt) (digest.Digest, []byte, error) {
	var img struct {
		Config struct {
			Env        []string `json:"Env,omitempty"`
			WorkingDir string   `json:"WorkingDir,omitempty"`
			User       string   `json:"User,omitempty"`
		} `json:"config,omitempty"`
	}
	r.called = true

	img.Config.WorkingDir = r.dir

	dt, err := json.Marshal(img)
	if err != nil {
		return "", nil, errors.WithStack(err)
	}
	return r.digest, dt, nil
}
