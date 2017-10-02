package llb

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/system"
	digest "github.com/opencontainers/go-digest"
)

type StateOption func(State) State

type Output interface {
	ToInput() (*pb.Input, error)
	Vertex() Vertex
}

type Vertex interface {
	Validate() error
	Marshal() ([]byte, *pb.OpMetadata, error)
	Output() Output
	Inputs() []Output
}

func NewState(o Output) State {
	s := State{
		out: o,
		ctx: context.Background(),
	}
	s = dir("/")(s)
	s = addEnv("PATH", system.DefaultPathEnv)(s)
	return s
}

type State struct {
	out Output
	ctx context.Context
}

func (s State) WithValue(k, v interface{}) State {
	return State{
		out: s.out,
		ctx: context.WithValue(s.ctx, k, v),
	}
}

func (s State) Value(k interface{}) interface{} {
	return s.ctx.Value(k)
}

func (s State) Marshal() (*Definition, error) {
	def := &Definition{
		Metadata: make(map[digest.Digest]OpMetadata, 0),
	}
	def, err := marshal(s.Output().Vertex(), def, map[digest.Digest]struct{}{}, map[Vertex]struct{}{})
	if err != nil {
		return def, err
	}
	inp, err := s.Output().ToInput()
	if err != nil {
		return def, err
	}
	proto := &pb.Op{Inputs: []*pb.Input{inp}}
	dt, err := proto.Marshal()
	if err != nil {
		return def, err
	}
	def.Def = append(def.Def, dt)
	return def, nil
}

func marshal(v Vertex, def *Definition, cache map[digest.Digest]struct{}, vertexCache map[Vertex]struct{}) (*Definition, error) {
	for _, inp := range v.Inputs() {
		var err error
		def, err = marshal(inp.Vertex(), def, cache, vertexCache)
		if err != nil {
			return def, err
		}
	}
	if _, ok := vertexCache[v]; ok {
		return def, nil
	}

	dt, opMeta, err := v.Marshal()
	if err != nil {
		return def, err
	}
	vertexCache[v] = struct{}{}
	dgst := digest.FromBytes(dt)
	if _, ok := cache[dgst]; ok {
		return def, nil
	}
	def.Def = append(def.Def, dt)
	if opMeta != nil {
		def.Metadata[dgst] = OpMetadata{*opMeta}
	}
	cache[dgst] = struct{}{}
	return def, nil
}

func (s State) Validate() error {
	return s.Output().Vertex().Validate()
}

func (s State) Output() Output {
	return s.out
}

func (s State) WithOutput(o Output) State {
	return State{
		out: o,
		ctx: s.ctx,
	}
}

func (s State) Run(ro ...RunOption) ExecState {
	ei := ExecInfo{State: s}
	for _, o := range ro {
		ei = o(ei)
	}
	meta := Meta{
		Args: getArgs(ei.State),
		Cwd:  getDir(ei.State),
		Env:  getEnv(ei.State),
	}

	exec := NewExecOp(s.Output(), meta, ei.ReadonlyRootFS)
	for _, m := range ei.Mounts {
		exec.AddMount(m.Target, m.Source, m.Opts...)
	}

	return ExecState{
		State: s.WithOutput(exec.Output()),
		exec:  exec,
	}
}

func (s State) AddEnv(key, value string) State {
	return s.AddEnvf(key, value)
}

func (s State) AddEnvf(key, value string, v ...interface{}) State {
	return addEnvf(key, value, v...)(s)
}

func (s State) Dir(str string) State {
	return s.Dirf(str)
}
func (s State) Dirf(str string, v ...interface{}) State {
	return dirf(str, v...)(s)
}

func (s State) GetEnv(key string) (string, bool) {
	return getEnv(s).Get(key)
}

func (s State) GetDir() string {
	return getDir(s)
}

func (s State) GetArgs() []string {
	return getArgs(s)
}

func (s State) Reset(s2 State) State {
	return reset(s2)(s)
}

func (s State) With(so ...StateOption) State {
	for _, o := range so {
		s = o(s)
	}
	return s
}

type output struct {
	vertex   Vertex
	getIndex func() (pb.OutputIndex, error)
}

func (o *output) ToInput() (*pb.Input, error) {
	var index pb.OutputIndex
	if o.getIndex != nil {
		var err error
		index, err = o.getIndex()
		if err != nil {
			return nil, err
		}
	}
	dt, opMeta, err := o.vertex.Marshal()
	_ = opMeta
	if err != nil {
		return nil, err
	}
	dgst := digest.FromBytes(dt)
	return &pb.Input{Digest: dgst, Index: index}, nil
}

func (o *output) Vertex() Vertex {
	return o.vertex
}
