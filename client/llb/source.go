package llb

import (
	_ "crypto/sha256"

	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
)

type SourceOp struct {
	id     string
	attrs  map[string]string
	output Output
}

func NewSource(id string, attrs map[string]string) *SourceOp {
	s := &SourceOp{
		id:    id,
		attrs: attrs,
	}
	s.output = &output{vertex: s}
	return s
}

func (s *SourceOp) Validate() error {
	if s.id == "" {
		return errors.Errorf("source identifier can't be empty")
	}
	return nil
}

func (s *SourceOp) Marshal() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	proto := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{Identifier: s.id, Attrs: s.attrs},
		},
	}
	return proto.Marshal()
}

func (s *SourceOp) Output() Output {
	return s.output
}

func (s *SourceOp) Inputs() []Output {
	return nil
}

func Source(id string) State {
	return NewState(NewSource(id, nil).Output())
}

func Image(ref string) State {
	return Source("docker-image://" + ref) // controversial
}

func Git(remote, ref string, opts ...GitOption) State {
	id := remote
	if ref != "" {
		id += "#" + ref
	}

	gi := &GitInfo{}
	for _, o := range opts {
		o(gi)
	}
	attrs := map[string]string{}
	if gi.KeepGitDir {
		attrs[pb.AttrKeepGitDir] = "true"
	}

	source := NewSource("git://"+id, attrs)
	return NewState(source.Output())
}

type GitOption func(*GitInfo)

type GitInfo struct {
	KeepGitDir bool
}

func KeepGitDir() GitOption {
	return func(gi *GitInfo) {
		gi.KeepGitDir = true
	}
}

func Scratch() State {
	return NewState(nil)
}

func Local(name string, opts ...LocalOption) State {
	gi := &LocalInfo{}

	for _, o := range opts {
		o(gi)
	}
	attrs := map[string]string{}
	if gi.SessionID != "" {
		attrs[pb.AttrLocalSessionID] = gi.SessionID
	}

	source := NewSource("local://"+name, attrs)
	return NewState(source.Output())
}

type LocalOption func(*LocalInfo)

func SessionID(id string) LocalOption {
	return func(li *LocalInfo) {
		li.SessionID = id
	}
}

type LocalInfo struct {
	SessionID string
}
