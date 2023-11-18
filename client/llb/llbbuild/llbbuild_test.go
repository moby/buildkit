package llbbuild

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMarshal(t *testing.T) {
	t.Parallel()
	b := NewBuildOp(newDummyOutput("foobar"), WithFilename("myfilename"))
	dgst, dt, opMeta, _, err := b.Marshal(context.TODO(), &llb.Constraints{})
	_ = opMeta
	require.NoError(t, err)

	require.Equal(t, dgst, digest.FromBytes(dt))

	var op pb.Op
	err = proto.Unmarshal(dt, &op)
	require.NoError(t, err)

	buildop := op.GetBuild()
	require.NotEqual(t, buildop, nil)

	require.Equal(t, len(op.Inputs), 1)
	require.Equal(t, buildop.Builder, int64(pb.LLBBuilder))
	require.Equal(t, len(buildop.Inputs), 1)
	require.Equal(t, buildop.Inputs[pb.LLBDefinitionInput], &pb.BuildInput{Input: 0})

	require.Equal(t, buildop.Attrs[pb.AttrLLBDefinitionFilename], "myfilename")
}

func newDummyOutput(key string) llb.Output {
	dgst := digest.FromBytes([]byte(key))
	return &dummyOutput{dgst: dgst}
}

type dummyOutput struct {
	dgst digest.Digest
}

func (d *dummyOutput) ToInput(context.Context, *llb.Constraints) (*pb.Input, error) {
	return &pb.Input{
		Digest: d.dgst.String(),
		Index:  7, // random constant
	}, nil
}
func (d *dummyOutput) Vertex(context.Context, *llb.Constraints) llb.Vertex {
	return nil
}
