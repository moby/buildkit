package llb

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type MergeOp struct {
	MarshalCache
	inputs      []Output
	output      Output
	constraints Constraints
}

func NewMerge(inputs []State, c Constraints) *MergeOp {
	op := &MergeOp{constraints: c}
	for _, input := range inputs {
		op.inputs = append(op.inputs, input.Output())
	}
	op.output = &output{vertex: op}
	return op
}

func (m *MergeOp) Validate(ctx context.Context, constraints *Constraints) error {
	if len(m.inputs) < 2 {
		return errors.Errorf("merge must have at least 2 inputs")
	}
	return nil
}

func (m *MergeOp) Marshal(ctx context.Context, constraints *Constraints) (digest.Digest, []byte, *pb.OpMetadata, []*SourceLocation, error) {
	if m.Cached(constraints) {
		return m.Load()
	}
	if err := m.Validate(ctx, constraints); err != nil {
		return "", nil, nil, nil, err
	}

	proto, md := MarshalConstraints(constraints, &m.constraints)
	proto.Platform = nil // merge op is not platform specific

	op := &pb.MergeOp{}
	for _, input := range m.inputs {
		op.Inputs = append(op.Inputs, &pb.MergeInput{Input: pb.InputIndex(len(proto.Inputs))})
		pbInput, err := input.ToInput(ctx, constraints)
		if err != nil {
			return "", nil, nil, nil, err
		}
		proto.Inputs = append(proto.Inputs, pbInput)
	}
	proto.Op = &pb.Op_Merge{Merge: op}

	dt, err := proto.Marshal()
	if err != nil {
		return "", nil, nil, nil, err
	}

	m.Store(dt, md, m.constraints.SourceLocations, constraints)
	return m.Load()
}

func (m *MergeOp) Output() Output {
	return m.output
}

func (m *MergeOp) Inputs() []Output {
	return m.inputs
}

func Merge(inputs []State, opts ...ConstraintsOpt) State {
	var c Constraints
	for _, o := range opts {
		o.SetConstraintsOption(&c)
	}
	return NewState(NewMerge(inputs, c).Output())
}
