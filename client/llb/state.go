package llb

import (
	_ "crypto/sha256"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type StateOption func(s *State) *State

// State represents modifiable llb state
type State struct {
	source   *source
	exec     *exec
	meta     Meta
	mount    *mount
	metaNext Meta // this meta will be used for the next Run()
	err      error
}

// TODO: add state.Reset() state.Save() state.Restore()

// Validate checks that every node has been set up properly
func (s *State) Validate() error {
	if s.source != nil {
		if err := s.source.Validate(); err != nil {
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

	exec := &exec{
		meta:   meta,
		mounts: []*mount{},
		root: &mount{
			dest:      pb.RootMount,
			source:    s.source,
			parent:    s.mount,
			hasOutput: true,
		},
	}
	exec.root.execState = &es
	exec.mounts = append(exec.mounts, exec.root)

	es.exec = exec
	es.mount = exec.root
	es.meta = meta
	es.metaNext = es.meta
	var err error
	for _, o := range opts {
		es = *o(&es)
	}
	es.exec.meta = es.meta
	es.err = err
	return &es
}

func (s *State) AddEnv(key, value string) *State {
	return s.AddEnvf(key, value)
}
func (s *State) AddEnvf(key, value string, v ...interface{}) *State {
	s.metaNext, _ = addEnvf(key, value, v...)(s.metaNext)
	return s
}

func (s *State) DelEnv(key string) *State {
	s.metaNext, _ = delEnv(key)(s.metaNext)
	return s
}
func (s *State) ClearEnv() *State {
	s.metaNext, _ = clearEnv()(s.metaNext)
	return s
}
func (s *State) GetEnv(key string) (string, bool) {
	return s.metaNext.Env(key)
}

func (s *State) Dir(str string) *State {
	return s.Dirf(str)
}
func (s *State) Dirf(str string, v ...interface{}) *State {
	s.metaNext, _ = dirf(str, v...)(s.metaNext)
	return s
}

func (s *State) GetDir() string {
	return s.metaNext.Dir()
}

func (s *State) Args(arg ...string) *State {
	s.metaNext, _ = args(arg...)(s.metaNext)
	return s
}

func (s *State) Reset(src *State) *State {
	copy := *s
	copy.metaNext, _ = reset(src)(s.metaNext)
	return &copy
}

func (s *State) With(so ...StateOption) *State {
	for _, o := range so {
		s = o(s)
	}
	return s
}

func (s *State) Marshal() (list [][]byte, err error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	cache := make(map[digest.Digest]struct{})
	if s.source != nil {
		_, list, err = s.source.marshalTo(nil, cache)
	} else if s.exec != nil {
		_, list, err = s.exec.root.marshalTo(nil, cache)
	} else {
		_, list, err = s.mount.marshalTo(nil, cache)
	}
	return
}

// ExecState is a state with a active leaf pointing to a run exec command.
// Mounts can be added only to this state.
type ExecState struct {
	State
}

func (s *ExecState) AddMount(dest string, mountState *State, opts ...MountOption) *State {
	m := &mount{
		dest:      dest,
		source:    mountState.source,
		parent:    mountState.mount,
		execState: s,
		hasOutput: true, // TODO: should be set only if something inherits
	}
	for _, opt := range opts {
		opt(m)
	}
	var newState State
	newState.meta = s.meta
	newState.metaNext = s.metaNext
	newState.mount = m
	s.exec.mounts = append(s.exec.mounts, m)
	return &newState
}

func (s *ExecState) Root() *State {
	return &s.State
}

func (s *ExecState) GetMount(target string) (*State, error) {
	for _, m := range s.exec.mounts {
		if m.dest == target {
			var newState State
			newState.meta = m.execState.meta
			newState.metaNext = m.execState.metaNext
			newState.mount = m
			return &newState, nil
		}
	}
	return nil, errors.WithStack(errNotFound)
}

func (s *ExecState) updateMeta(fn metaOption) *ExecState {
	meta, err := fn(s.meta)
	s.meta = meta
	if err != nil {
		s.err = err
	}
	return s
}

type RunOption func(es *ExecState) *ExecState

type MountOption func(*mount)

func Readonly(m *mount) {
	m.readonly = true
}

func AddMount(dest string, mountState *State, opts ...MountOption) RunOption {
	return func(es *ExecState) *ExecState {
		es.AddMount(dest, mountState, opts...)
		return nil
	}
}

func Shlex(str string) RunOption {
	return Shlexf(str)
}
func Shlexf(str string, v ...interface{}) RunOption {
	return func(es *ExecState) *ExecState {
		return es.updateMeta(shlexf(str, v...))
	}
}

func AddEnv(key, value string) RunOption {
	return AddEnvf(key, value)
}
func AddEnvf(key, value string, v ...interface{}) RunOption {
	return func(es *ExecState) *ExecState {
		return es.updateMeta(addEnvf(key, value, v...))
	}
}

func DelEnv(key string) RunOption {
	return func(es *ExecState) *ExecState {
		return es.updateMeta(delEnv(key))
	}
}
func ClearEnv() RunOption {
	return func(es *ExecState) *ExecState {
		return es.updateMeta(clearEnv())
	}
}

func Dir(str string) RunOption {
	return Dirf(str)
}
func Dirf(str string, v ...interface{}) RunOption {
	return func(es *ExecState) *ExecState {
		return es.updateMeta(dirf(str, v...))
	}
}

func Args(arg ...string) RunOption {
	return func(es *ExecState) *ExecState {
		return es.updateMeta(args(arg...))
	}
}

func Reset(src *State) RunOption {
	return func(es *ExecState) *ExecState {
		return es.updateMeta(reset(src))
	}
}
