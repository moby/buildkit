package llb

import (
	_ "crypto/sha256"
	"fmt"
	"sort"

	"github.com/gogo/protobuf/proto"
	"github.com/google/shlex"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/solver/pb"
	"github.com/tonistiigi/buildkit_poc/util/system"
)

type RunOption func(m Meta) Meta

type SourceOp struct {
	id string
}

type ExecOp struct {
	meta   Meta
	mounts []*Mount
	root   *Mount
}

type Meta struct {
	Args []string
	Env  []string
	Cwd  string
}

type Mount struct {
	op         *ExecOp
	dest       string
	mount      *Mount
	src        *SourceOp
	output     bool
	inputIndex int64
}

func NewMeta(args ...string) Meta {
	m := Meta{}
	m = m.addEnv("PATH", system.DefaultPathEnv)
	m = m.setArgs(args...)
	m.Cwd = "/"
	return m
}

func (m *Meta) ensurePrivate() {
	m.Env = append([]string{}, m.Env...)
	m.Args = append([]string{}, m.Args...)
}

func (m Meta) addEnv(k, v string) Meta {
	(&m).ensurePrivate()
	// TODO: flatten
	m.Env = append(m.Env, k+"="+v)
	return m
}

func (m Meta) setArgs(args ...string) Meta {
	m.Args = append([]string{}, args...)
	return m
}

func Shlex(str string, v ...string) RunOption {
	return func(m Meta) Meta {
		vi := make([]interface{}, 0, len(v))
		for _, v := range v {
			vi = append(vi, v)
		}
		sp, err := shlex.Split(fmt.Sprintf(str, vi...))
		if err != nil {
			panic(err) // TODO
		}
		(&m).ensurePrivate()
		return m.setArgs(sp...)
	}
}

type State struct {
	src      *SourceOp
	exec     *ExecOp
	meta     Meta
	mount    *Mount
	metaNext Meta
}

type ExecState struct {
	State
}

func (s *State) Validate() error {
	if s.src != nil {
		if err := s.src.Validate(); err != nil {
			return err
		}
	}
	if s.exec != nil {
		if err := s.exec.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) Run(opts ...RunOption) *ExecState {
	var es ExecState
	meta := s.metaNext
	for _, o := range opts {
		meta = o(meta)
	}
	exec := newExec(meta, s.src, s.mount)
	es.exec = exec
	es.mount = exec.root
	es.metaNext = meta
	es.meta = meta
	return &es
}

func (s *State) AddEnv(k, v string) *State {
	s.metaNext = s.metaNext.addEnv(k, v)
	return s
}
func (s *State) Dir(wd string) *State {
	s.metaNext.Cwd = wd
	return s
}

func (s *State) Marshal() ([][]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	cache := make(map[digest.Digest]struct{})
	var list [][]byte
	var err error
	if s.src != nil { // TODO: fix repetition
		_, list, err = s.src.recursiveMarshal(nil, cache)
	} else if s.exec != nil {
		_, list, err = s.exec.root.recursiveMarshal(nil, cache)
	} else {
		_, list, err = s.mount.recursiveMarshal(nil, cache)
	}
	return list, err
}

func (s *ExecState) AddMount(dest string, mount *State) *State {
	m := &Mount{
		dest:   dest,
		src:    mount.src,
		mount:  mount.mount,
		op:     s.exec,
		output: true, // TODO: should be set only if something inherits
	}
	var newState State
	newState.meta = s.meta
	newState.metaNext = s.meta
	newState.mount = m
	s.exec.mounts = append(s.exec.mounts, m)
	return &newState
}

func (s *ExecState) Root() *State {
	return &s.State
}

func newExec(meta Meta, src *SourceOp, m *Mount) *ExecOp {
	exec := &ExecOp{
		meta:   meta,
		mounts: []*Mount{},
		root: &Mount{
			dest:   "/",
			src:    src,
			mount:  m,
			output: true,
		},
	}
	exec.root.op = exec
	exec.mounts = append(exec.mounts, exec.root)
	return exec
}

func Source(id string) *State {
	return &State{
		metaNext: NewMeta(),
		src:      &SourceOp{id: id},
	}
}

func (so *SourceOp) Validate() error {
	// TODO: basic identifier validation
	if so.id == "" {
		return errors.Errorf("source identifier can't be empty")
	}
	return nil
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
	return appendResult(po, list, cache)
}

func Image(ref string) *State {
	return Source("docker-image://" + ref) // controversial
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
		var op interface{}
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
			if pop.Inputs[i].Digest == dgst {
				inputIndex = i
				break
			}
		}
		if inputIndex == len(pop.Inputs) {
			var mountIndex int64
			if m.mount != nil {
				mountIndex = m.mount.inputIndex
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
		if m.output {
			pm.Output = outputIndex
			outputIndex++
		} else {
			pm.Output = -1
		}
		m.inputIndex = outputIndex - 1
		peo.Mounts = append(peo.Mounts, pm)
	}

	return appendResult(pop, list, cache)
}

func (m *Mount) recursiveMarshal(list [][]byte, cache map[digest.Digest]struct{}) (digest.Digest, [][]byte, error) {
	if m.op == nil {
		return "", nil, errors.Errorf("invalid mount")
	}
	var dgst digest.Digest
	dgst, list, err := m.op.recursiveMarshal(list, cache)
	if err != nil {
		return "", list, err
	}
	for _, m2 := range m.op.mounts {
		if m2 == m {
			po := &pb.Op{}
			po.Inputs = append(po.Inputs, &pb.Input{
				Digest: dgst,
				Index:  int64(m.inputIndex),
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

func recursiveMarshalAny(op interface{}, list [][]byte, cache map[digest.Digest]struct{}) (dgst digest.Digest, out [][]byte, err error) {
	switch op := op.(type) {
	case *ExecOp:
		return op.recursiveMarshal(list, cache)
	case *SourceOp:
		return op.recursiveMarshal(list, cache)
	default:
		return "", nil, errors.Errorf("invalid operation")
	}
}
