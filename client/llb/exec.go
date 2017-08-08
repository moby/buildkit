package llb

import (
	_ "crypto/sha256"
	"sort"

	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
)

type Meta struct {
	Args []string
	Env  EnvList
	Cwd  string
}

func NewExecOp(root Output, meta Meta) *ExecOp {
	e := &ExecOp{meta: meta}
	rootMount := &mount{
		target: pb.RootMount,
		source: root,
	}
	e.mounts = append(e.mounts, rootMount)
	e.root = &output{vertex: e, getIndex: e.getMountIndexFn(rootMount)}
	rootMount.output = e.root

	return e
}

type mount struct {
	target   string
	readonly bool
	source   Output
	output   Output
	selector string
	// hasOutput bool
}

type ExecOp struct {
	root   Output
	mounts []*mount
	meta   Meta
}

func (e *ExecOp) AddMount(target string, source Output, opt ...MountOption) Output {
	m := &mount{
		target: target,
		source: source,
	}
	for _, o := range opt {
		o(m)
	}
	e.mounts = append(e.mounts, m)
	m.output = &output{vertex: e, getIndex: e.getMountIndexFn(m)}
	return m.output
}

func (e *ExecOp) GetMount(target string) Output {
	for _, m := range e.mounts {
		if m.target == target {
			return m.output
		}
	}
	return nil
}

func (e *ExecOp) Validate() error {
	if len(e.meta.Args) == 0 {
		return errors.Errorf("arguments are required")
	}
	if e.meta.Cwd == "" {
		return errors.Errorf("working directory is required")
	}
	for _, m := range e.mounts {
		if m.source != nil {
			if err := m.source.Vertex().Validate(); err != nil {
				return nil
			}
		}
	}
	return nil
}

func (e *ExecOp) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	// make sure mounts are sorted
	sort.Slice(e.mounts, func(i, j int) bool {
		return e.mounts[i].target < e.mounts[j].target
	})

	peo := &pb.ExecOp{
		Meta: &pb.Meta{
			Args: e.meta.Args,
			Env:  e.meta.Env.ToArray(),
			Cwd:  e.meta.Cwd,
		},
	}

	pop := &pb.Op{
		Op: &pb.Op_Exec{
			Exec: peo,
		},
	}

	for i, m := range e.mounts {
		inputIndex := pb.InputIndex(len(pop.Inputs))
		if m.source != nil {
			inp, err := m.source.ToInput()
			if err != nil {
				return nil, err
			}

			newInput := true

			for i, inp2 := range pop.Inputs {
				if *inp == *inp2 {
					inputIndex = pb.InputIndex(i)
					newInput = false
					break
				}
			}

			if newInput {
				pop.Inputs = append(pop.Inputs, inp)
			}
		} else {
			inputIndex = pb.Empty
		}

		pm := &pb.Mount{
			Input:    inputIndex,
			Dest:     m.target,
			Readonly: m.readonly,
			Output:   pb.OutputIndex(i),
			Selector: m.selector,
		}
		peo.Mounts = append(peo.Mounts, pm)
	}

	return pop.Marshal()
}

func (e *ExecOp) Output() Output {
	return e.root
}

func (e *ExecOp) Inputs() (inputs []Output) {
	mm := map[Output]struct{}{}
	for _, m := range e.mounts {
		if m.source != nil {
			mm[m.source] = struct{}{}
		}
	}
	for o := range mm {
		inputs = append(inputs, o)
	}
	return
}

func (e *ExecOp) getMountIndexFn(m *mount) func() (pb.OutputIndex, error) {
	return func() (pb.OutputIndex, error) {
		// make sure mounts are sorted
		sort.Slice(e.mounts, func(i, j int) bool {
			return e.mounts[i].target < e.mounts[j].target
		})

		for i, m2 := range e.mounts {
			if m == m2 {
				return pb.OutputIndex(i), nil
			}
		}
		return pb.OutputIndex(0), errors.Errorf("invalid mount")
	}
}

type ExecState struct {
	State
	exec *ExecOp
}

func (e ExecState) AddMount(target string, source State, opt ...MountOption) State {
	return NewState(e.exec.AddMount(target, source.Output(), opt...))
}

func (e ExecState) GetMount(target string) State {
	return NewState(e.exec.GetMount(target))
}

func (e ExecState) Root() State {
	return e.State
}

type MountOption func(*mount)

func Readonly(m *mount) {
	m.readonly = true
}

func SourcePath(src string) MountOption {
	return func(m *mount) {
		m.selector = src
	}
}

type RunOption func(es ExecInfo) ExecInfo

func Shlex(str string) RunOption {
	return Shlexf(str)
}
func Shlexf(str string, v ...interface{}) RunOption {
	return func(ei ExecInfo) ExecInfo {
		ei.State = shlexf(str, v...)(ei.State)
		return ei
	}
}

func AddEnv(key, value string) RunOption {
	return AddEnvf(key, value)
}

func AddEnvf(key, value string, v ...interface{}) RunOption {
	return func(ei ExecInfo) ExecInfo {
		ei.State = ei.State.AddEnvf(key, value, v...)
		return ei
	}
}

func Dir(str string) RunOption {
	return Dirf(str)
}
func Dirf(str string, v ...interface{}) RunOption {
	return func(ei ExecInfo) ExecInfo {
		ei.State = ei.State.Dirf(str, v...)
		return ei
	}
}

func Reset(s State) RunOption {
	return func(ei ExecInfo) ExecInfo {
		ei.State = ei.State.Reset(s)
		return ei
	}
}

func With(so ...StateOption) RunOption {
	return func(ei ExecInfo) ExecInfo {
		ei.State = ei.State.With(so...)
		return ei
	}
}

func AddMount(dest string, mountState State, opts ...MountOption) RunOption {
	return func(ei ExecInfo) ExecInfo {
		ei.Mounts = append(ei.Mounts, MountInfo{dest, mountState.Output(), opts})
		return ei
	}
}

type ExecInfo struct {
	State  State
	Mounts []MountInfo
}

type MountInfo struct {
	Target string
	Source Output
	Opts   []MountOption
}
