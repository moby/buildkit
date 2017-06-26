package llb

import (
	_ "crypto/sha256"

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
	var err error
	for _, o := range opts {
		meta, err = o(meta)
	}

	exec := &exec{
		meta:   meta,
		mounts: []*mount{},
		root: &mount{
			dest:      "/",
			source:    s.source,
			parent:    s.mount,
			hasOutput: true,
		},
	}
	exec.root.execState = &es
	exec.mounts = append(exec.mounts, exec.root)

	es.exec = exec
	es.mount = exec.root
	es.metaNext = meta
	es.meta = meta
	es.err = err
	return &es
}

func (s *State) AddEnv(key, value string, v ...interface{}) *State {
	s.metaNext, _ = AddEnv(key, value, v...)(s.metaNext)
	return s
}
func (s *State) DelEnv(key string) *State {
	s.metaNext, _ = DelEnv(key)(s.metaNext)
	return s
}
func (s *State) ClearEnv() *State {
	s.metaNext, _ = ClearEnv()(s.metaNext)
	return s
}
func (s *State) GetEnv(key string) (string, bool) {
	return s.metaNext.Env(key)
}

func (s *State) Dir(str string, v ...interface{}) *State {
	s.metaNext, _ = Dir(str, v...)(s.metaNext)
	return s
}
func (s *State) GetDir() string {
	return s.metaNext.Dir()
}

func (s *State) Args(args ...string) *State {
	s.metaNext, _ = Args(args...)(s.metaNext)
	return s
}

func (s *State) Reset(src *State) *State {
	copy := *s
	copy.metaNext, _ = Reset(src)(s.metaNext)
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

func (s *ExecState) AddMount(dest string, mountState *State) *State {
	m := &mount{
		dest:      dest,
		source:    mountState.source,
		parent:    mountState.mount,
		execState: s,
		hasOutput: true, // TODO: should be set only if something inherits
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
