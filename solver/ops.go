package solver

import (
	"sort"

	"github.com/gogo/protobuf/proto"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/solver/pb"
)

const noOutput = -1

type Op interface {
	Validate() error
	Marshal() ([][]byte, error)
	Run(meta Meta) *ExecOp
}

type SourceOp struct {
	id string
}

type ExecOp struct {
	meta   Meta
	mounts []*mount
	root   *mount
}

type Meta struct {
	Args []string
	Env  []string
	Cwd  string
}

type mount struct {
	op     *ExecOp
	dest   string
	mount  *mount
	src    *SourceOp
	output bool
}

func Source(id string) *SourceOp {
	return &SourceOp{id: id}
}

func (so *SourceOp) Validate() error {
	// TODO: basic identifier validation
	if so.id == "" {
		return errors.Errorf("source identifier can't be empty")
	}
	return nil
}

func (so *SourceOp) Run(m Meta) *ExecOp {
	return newExec(m, so, nil)
}

func (so *SourceOp) Marshal() ([][]byte, error) {
	if err := so.Validate(); err != nil {
		return nil, err
	}
	cache := make(map[digest.Digest]struct{})
	_, list, err := so.recursiveMarshal(nil, cache)
	return list, err
}

func (so *SourceOp) recursiveMarshal(list [][]byte, cache map[digest.Digest]struct{}) (digest.Digest, [][]byte, error) {
	if err := so.Validate(); err != nil {
		return "", nil, err
	}
	po := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{Identifier: so.id},
		},
	}
	return marshal(po, list, cache)
}

func newExec(meta Meta, src *SourceOp, m *mount) *ExecOp {
	exec := &ExecOp{
		meta:   meta,
		mounts: []*mount{},
		root: &mount{
			dest:  "/",
			src:   src,
			mount: m,
		},
	}
	exec.root.op = exec
	exec.mounts = append(exec.mounts, exec.root)
	return exec
}

func (eo *ExecOp) Validate() error {
	for _, m := range eo.mounts {
		if m.src != nil {
			if err := m.src.Validate(); err != nil {
				return err
			}
		}
		if m.mount != nil {
			if err := m.mount.op.Validate(); err != nil {
				return err
			}
		}
	}
	// TODO: validate meta
	return nil
}

func (eo *ExecOp) Run(meta Meta) *ExecOp {
	return newExec(meta, nil, eo.root)
}

func (eo *ExecOp) Marshal() ([][]byte, error) {
	if err := eo.Validate(); err != nil {
		return nil, err
	}
	cache := make(map[digest.Digest]struct{})
	_, list, err := eo.recursiveMarshal(nil, cache)
	return list, err
}

func (eo *ExecOp) recursiveMarshal(list [][]byte, cache map[digest.Digest]struct{}) (digest.Digest, [][]byte, error) {
	peo := &pb.ExecOp{
		Meta: &pb.Meta{
			Args: eo.meta.Args,
			Env:  eo.meta.Env,
			Cwd:  eo.meta.Cwd,
		},
	}

	pop := &pb.Op{
		Op: &pb.Op_Exec{
			Exec: peo,
		},
	}

	sort.Slice(eo.mounts, func(i, j int) bool {
		return eo.mounts[i].dest < eo.mounts[j].dest
	})

	var outputIndex int64 = 0

	for _, m := range eo.mounts {
		var dgst digest.Digest
		var err error
		var op Op
		if m.src != nil {
			op = m.src
		} else {
			op = m.mount.op
		}
		dgst, list, err = recursiveMarshalAny(op, list, cache)
		if err != nil {
			return "", nil, err
		}
		inputIndex := len(pop.Inputs)
		for i := range pop.Inputs {
			if pop.Inputs[i] == dgst.String() {
				inputIndex = i
				break
			}
		}

		pm := &pb.Mount{
			Input: int64(inputIndex),
			Dest:  m.dest,
		}
		if m.output {
			pm.Output = outputIndex
			outputIndex++
		} else {
			pm.Output = noOutput
		}
		peo.Mounts = append(peo.Mounts, pm)
	}

	return marshal(pop, list, cache)
}

func marshal(p proto.Marshaler, list [][]byte, cache map[digest.Digest]struct{}) (dgst digest.Digest, out [][]byte, err error) {
	dt, err := p.Marshal()
	if err != nil {
		return "", nil, err
	}
	dgst = digest.FromBytes(dt)
	if _, ok := cache[dgst]; ok {
		return dgst, list, nil
	}
	list = append(list, dt)
	cache[dgst] = struct{}{}
	return dgst, list, nil
}

func recursiveMarshalAny(op Op, list [][]byte, cache map[digest.Digest]struct{}) (dgst digest.Digest, out [][]byte, err error) {
	switch op := op.(type) {
	case *ExecOp:
		return op.recursiveMarshal(list, cache)
	case *SourceOp:
		return op.recursiveMarshal(list, cache)
	default:
		return "", nil, errors.Errorf("invalid operation")
	}
}
