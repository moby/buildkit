package llbbuild

import (
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
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
	return &build{source: source, info: info}
}

type build struct {
	source llb.Output
	info   *BuildInfo
}

func (b *build) ToInput() (*pb.Input, error) {
	dt, err := b.Marshal()
	if err != nil {
		return nil, err
	}
	dgst := digest.FromBytes(dt)
	return &pb.Input{Digest: dgst, Index: pb.OutputIndex(0)}, nil
}

func (b *build) Vertex() llb.Vertex {
	return b
}

func (b *build) Validate() error {
	return nil
}

func (b *build) Marshal() ([]byte, error) {
	pbo := &pb.BuildOp{
		Builder: pb.LLBBuilder,
		Inputs: map[string]*pb.BuildInput{
			pb.LLBDefinitionInput: {pb.InputIndex(0)}},
	}

	pbo.Attrs = map[string]string{}

	if b.info.DefinitionFilename != "" {
		pbo.Attrs[pb.AttrLLBDefinitionFilename] = b.info.DefinitionFilename
	}

	pop := &pb.Op{
		Op: &pb.Op_Build{
			Build: pbo,
		},
	}

	inp, err := b.source.ToInput()
	if err != nil {
		return nil, err
	}

	pop.Inputs = append(pop.Inputs, inp)

	return pop.Marshal()
}

func (b *build) Output() llb.Output {
	return b
}

func (b *build) Inputs() []llb.Output {
	return []llb.Output{b.source}
}

type BuildInfo struct {
	DefinitionFilename string
}

type BuildOption func(*BuildInfo)

func WithFilename(fn string) BuildOption {
	return func(b *BuildInfo) {
		b.DefinitionFilename = fn
	}
}
