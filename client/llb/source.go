package llb

import (
	"context"
	_ "crypto/sha256"
	"encoding/json"
	"strings"

	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
)

type SourceOp struct {
	id       string
	attrs    map[string]string
	output   Output
	cachedPB []byte
	err      error
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
	if s.err != nil {
		return s.err
	}
	if s.id == "" {
		return errors.Errorf("source identifier can't be empty")
	}
	return nil
}

func (s *SourceOp) Marshal() ([]byte, error) {
	if s.cachedPB != nil {
		return s.cachedPB, nil
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}

	proto := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{Identifier: s.id, Attrs: s.attrs},
		},
	}
	dt, err := proto.Marshal()
	if err != nil {
		return nil, err
	}
	s.cachedPB = dt
	return dt, nil
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

func Image(ref string, opts ...ImageOption) State {
	r, err := reference.ParseNormalizedNamed(ref)
	if err == nil {
		ref = reference.TagNameOnly(r).String()
	}
	src := NewSource("docker-image://"+ref, nil) // controversial
	if err != nil {
		src.err = err
	}
	var info ImageInfo
	for _, opt := range opts {
		opt(&info)
	}
	if info.metaResolver != nil {
		img, err := info.metaResolver.ResolveImageConfig(context.TODO(), ref)
		if err != nil {
			src.err = err
		} else {
			st := NewState(src.Output())
			for _, env := range img.Config.Env {
				parts := strings.SplitN(env, "=", 2)
				if len(parts[0]) > 0 {
					var v string
					if len(parts) > 1 {
						v = parts[1]
					}
					st = st.AddEnv(parts[0], v)
				}
			}
			st = st.Dir(img.Config.WorkingDir)
			return st
		}
	}
	return NewState(src.Output())
}

type ImageOption func(*ImageInfo)

type ImageInfo struct {
	metaResolver ImageMetaResolver
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
	if gi.IncludePatterns != "" {
		attrs[pb.AttrIncludePatterns] = gi.IncludePatterns
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

func IncludePatterns(p []string) LocalOption {
	return func(li *LocalInfo) {
		dt, _ := json.Marshal(p) // empty on error
		li.IncludePatterns = string(dt)
	}
}

type LocalInfo struct {
	SessionID       string
	IncludePatterns string
}
