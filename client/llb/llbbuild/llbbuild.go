package llbbuild

import (
	"context"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"google.golang.org/protobuf/proto"
)

func Build(opt ...BuildOption) llb.StateOption {
	return func(s llb.State) llb.State {
		return s.WithOutput(NewBuildOp(s.Output(), opt...).Output())
	}
}

func NewBuildOp(source llb.Output, opt ...BuildOption) llb.Vertex {
	info := &BuildInfo{}
	for _, o := range opt {
		o(info)
	}
	return &build{source: source, info: info, constraints: info.Constraints}
}

type build struct {
	llb.MarshalCache
	source      llb.Output
	info        *BuildInfo
	constraints llb.Constraints
}

func (b *build) ToInput(ctx context.Context, c *llb.Constraints) (*pb.Input, error) {
	dgst, _, _, _, err := b.Marshal(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.Input{Digest: dgst.String(), Index: 0}, nil
}

func (b *build) Vertex(context.Context, *llb.Constraints) llb.Vertex {
	return b
}

func (b *build) Validate(context.Context, *llb.Constraints) error {
	return nil
}

func (b *build) Marshal(ctx context.Context, c *llb.Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*llb.SourceLocation, error) {
	if b.Cached(c) {
		return b.Load()
	}
	pbo := &pb.BuildOp{
		Builder: int64(pb.LLBBuilder),
		Inputs: map[string]*pb.BuildInput{
			pb.LLBDefinitionInput: {Input: 0}},
	}

	pbo.Attrs = map[string]string{}

	if b.info.DefinitionFilename != "" {
		pbo.Attrs[pb.AttrLLBDefinitionFilename] = b.info.DefinitionFilename
	}

	if b.constraints.Metadata == nil {
		b.constraints.Metadata = &pb.OpMetadata{}
	}
	if b.constraints.Metadata.Caps == nil {
		b.constraints.Metadata.Caps = make(map[string]bool)
	}
	b.constraints.Metadata.Caps[string(pb.CapBuildOpLLBFileName)] = true

	pop, md := llb.MarshalConstraints(c, &b.constraints)
	pop.Op = &pb.Op_Build{
		Build: pbo,
	}

	inp, err := b.source.ToInput(ctx, c)
	if err != nil {
		return "", nil, nil, nil, err
	}

	pop.Inputs = append(pop.Inputs, inp)

	dt, err := proto.Marshal(pop)
	if err != nil {
		return "", nil, nil, nil, err
	}
	b.Store(dt, md, b.constraints.SourceLocations, c)
	return b.Load()
}

func (b *build) Output() llb.Output {
	return b
}

func (b *build) Inputs() []llb.Output {
	return []llb.Output{b.source}
}

type BuildInfo struct {
	llb.Constraints
	DefinitionFilename string
}

type BuildOption func(*BuildInfo)

func WithFilename(fn string) BuildOption {
	return func(b *BuildInfo) {
		b.DefinitionFilename = fn
	}
}

func WithConstraints(co llb.ConstraintsOpt) BuildOption {
	return func(b *BuildInfo) {
		co.SetConstraintsOption(&b.Constraints)
	}
}
