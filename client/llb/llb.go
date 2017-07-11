package llb

import (
	_ "crypto/sha256"
	"sort"

	"github.com/gogo/protobuf/proto"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

var errNotFound = errors.Errorf("not found")

func Source(id string) *State {
	return &State{
		metaNext: NewMeta(),
		source:   &source{id: id, attrs: map[string]string{}},
	}
}

type source struct {
	id      string
	attrs   map[string]string
	scratch bool
}

func (so *source) Validate() error {
	// TODO: basic identifier validation
	if so.id == "" {
		return errors.Errorf("source identifier can't be empty")
	}
	return nil
}

func (so *source) marshalTo(list [][]byte, cache map[digest.Digest]struct{}) (digest.Digest, [][]byte, error) {
	if so.scratch {
		return "", list, nil
	}
	if err := so.Validate(); err != nil {
		return "", nil, err
	}
	po := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{Identifier: so.id, Attrs: so.attrs},
		},
	}
	return appendResult(po, list, cache)
}

func Image(ref string) *State {
	return Source("docker-image://" + ref) // controversial
}

func Git(remote, ref string, opts ...GitOption) *State {
	id := remote
	if ref != "" {
		id += "#" + ref
	}
	state := Source("git://" + id)
	for _, opt := range opts {
		opt(state.source)
	}
	return state
}

type GitOption func(*source)

func KeepGitDir() GitOption {
	return func(s *source) {
		s.attrs[pb.AttrKeepGitDir] = "true"
	}
}

func Scratch() *State {
	s := Source("scratch")
	s.source.scratch = true
	return s
}

func Local(name string) *State {
	return Source("local://" + name)
}

type LocalOption func(*source)

func SessionID(id string) LocalOption {
	return func(s *source) {
		s.attrs[pb.AttrLocalSessionID] = id
	}
}

type exec struct {
	meta   Meta
	mounts []*mount
	root   *mount
}

func (eo *exec) Validate() error {
	for _, m := range eo.mounts {
		if m.source != nil {
			if err := m.source.Validate(); err != nil {
				return err
			}
		}
		if m.parent != nil {
			if err := m.parent.execState.exec.Validate(); err != nil {
				return err
			}
		}
	}
	// TODO: validate meta
	return nil
}

func (eo *exec) marshalTo(list [][]byte, cache map[digest.Digest]struct{}) (digest.Digest, [][]byte, error) {
	peo := &pb.ExecOp{
		Meta: &pb.Meta{
			Args: eo.meta.args,
			Env:  eo.meta.env.ToArray(),
			Cwd:  eo.meta.cwd,
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
		if m.source != nil {
			dgst, list, err = m.source.marshalTo(list, cache)
		} else {
			dgst, list, err = m.parent.execState.exec.marshalTo(list, cache)
		}
		if err != nil {
			return "", list, err
		}
		inputIndex := len(pop.Inputs)
		for i := range pop.Inputs {
			if pop.Inputs[i].Digest == dgst {
				inputIndex = i
				break
			}
		}
		if dgst == "" {
			inputIndex = -1
		}
		if inputIndex == len(pop.Inputs) {
			var mountIndex int64
			if m.parent != nil {
				mountIndex = m.parent.outputIndex
			}
			pop.Inputs = append(pop.Inputs, &pb.Input{
				Digest: dgst,
				Index:  mountIndex,
			})
		}

		pm := &pb.Mount{
			Input: int64(inputIndex),
			Dest:  m.dest,
		}
		if m.hasOutput {
			pm.Output = outputIndex
			outputIndex++
		} else {
			pm.Output = -1
		}
		m.outputIndex = outputIndex - 1
		peo.Mounts = append(peo.Mounts, pm)
	}

	return appendResult(pop, list, cache)
}

type mount struct {
	execState *ExecState
	dest      string
	// ro bool
	// either parent or source has to be set
	parent      *mount
	source      *source
	hasOutput   bool  // TODO: remove
	outputIndex int64 // filled in after marshal
	state       *ExecState
}

func (m *mount) marshalTo(list [][]byte, cache map[digest.Digest]struct{}) (digest.Digest, [][]byte, error) {
	if m.execState == nil {
		return "", nil, errors.Errorf("invalid mount")
	}
	var dgst digest.Digest
	dgst, list, err := m.execState.exec.marshalTo(list, cache)
	if err != nil {
		return "", list, err
	}
	for _, m2 := range m.execState.exec.mounts {
		if m2 == m {
			po := &pb.Op{}
			po.Inputs = append(po.Inputs, &pb.Input{
				Digest: dgst,
				Index:  int64(m.outputIndex),
			})
			return appendResult(po, list, cache)
		}
	}
	return "", nil, errors.Errorf("invalid mount")
}

func appendResult(p proto.Marshaler, list [][]byte, cache map[digest.Digest]struct{}) (dgst digest.Digest, out [][]byte, err error) {
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
