package llb

import (
	_ "crypto/sha256"
	"os"
	"path"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// Examples:
// local := llb.Local(...)
// llb.Image().Dir("/abc").File(Mkdir("./foo").Mkfile("/abc/foo/bar", []byte("data")))
// llb.Image().File(Mkdir("/foo").Mkfile("/foo/bar", []byte("data")))
// llb.Image().File(Copy(local, "/foo", "/bar")).File(Copy(local, "/foo2", "/bar2"))
//
// a := Mkdir("./foo")  // *FileAction /ced/foo
// b := Mkdir("./bar") // /abc/bar
// c := b.Copy(a.WithState(llb.Scratch().Dir("/ced")), "./foo", "./baz") // /abc/baz
// llb.Image().Dir("/abc").File(c)
//
// In future this can be extended to multiple outputs with:
// a := Mkdir("./foo")
// b, id := a.GetSelector()
// c := b.Mkdir("./bar")
// filestate = state.File(c)
// filestate.GetOutput(id).Exec()

func NewFileOp(s State, action *FileAction) *FileOp {
	action = action.bind(s)

	f := &FileOp{
		action: action,
	}

	f.output = &output{vertex: f, getIndex: func() (pb.OutputIndex, error) {
		return pb.OutputIndex(0), nil
	}}

	return f
}

// CopyInput is either llb.State or *FileActionWithState
type CopyInput interface {
	isFileOpCopyInput()
}

type subAction interface {
	toProtoAction(string, pb.InputIndex) pb.IsFileAction
}

type FileAction struct {
	state  *State
	prev   *FileAction
	action subAction
	err    error
}

func (fa *FileAction) Mkdir(p string, m os.FileMode, opt ...MkdirOption) *FileAction {
	a := Mkdir(p, m, opt...)
	a.prev = fa
	return a
}

func (fa *FileAction) Mkfile(p string, m os.FileMode, dt []byte, opt ...MkfileOption) *FileAction {
	a := Mkfile(p, m, dt, opt...)
	a.prev = fa
	return a
}

func (fa *FileAction) Rm(p string, opt ...RmOption) *FileAction {
	a := Rm(p, opt...)
	a.prev = fa
	return a
}

func (fa *FileAction) Copy(input CopyInput, src, dest string, opt ...CopyOption) *FileAction {
	a := Copy(input, src, dest, opt...)
	a.prev = fa
	return a
}

func (fa *FileAction) allOutputs(m map[Output]struct{}) {
	if fa == nil {
		return
	}
	if fa.state != nil && fa.state.Output() != nil {
		m[fa.state.Output()] = struct{}{}
	}

	if a, ok := fa.action.(*fileActionCopy); ok {
		if a.state != nil {
			if out := a.state.Output(); out != nil {
				m[out] = struct{}{}
			}
		} else if a.fas != nil {
			a.fas.allOutputs(m)
		}
	}
	fa.prev.allOutputs(m)
}

func (fa *FileAction) bind(s State) *FileAction {
	if fa == nil {
		return nil
	}
	fa2 := *fa
	fa2.prev = fa.prev.bind(s)
	fa2.state = &s
	return &fa2
}

func (fa *FileAction) WithState(s State) CopyInput {
	return &fileActionWithState{FileAction: fa.bind(s)}
}

type fileActionWithState struct {
	*FileAction
}

func (fas *fileActionWithState) isFileOpCopyInput() {}

func Mkdir(p string, m os.FileMode, opt ...MkdirOption) *FileAction {
	var mi MkdirInfo
	for _, o := range opt {
		o.SetMkdirOption(&mi)
	}
	return &FileAction{
		action: &fileActionMkdir{
			file: p,
			mode: m,
			info: mi,
		},
	}
}

type fileActionMkdir struct {
	file string
	mode os.FileMode
	info MkdirInfo
}

func (a *fileActionMkdir) toProtoAction(parent string, base pb.InputIndex) pb.IsFileAction {
	return &pb.FileAction_Mkdir{
		Mkdir: &pb.FileActionMkDir{
			Path:        normalizePath(parent, a.file),
			Mode:        int32(a.mode & 0777),
			MakeParents: a.info.MakeParents,
			Owner:       a.info.ChownOpt.marshal(base),
		},
	}
}

type MkdirOption interface {
	SetMkdirOption(*MkdirInfo)
}

type ChownOption interface {
	MkdirOption
	MkfileOption
	CopyOption
}

type mkdirOptionFunc func(*MkdirInfo)

func (fn mkdirOptionFunc) SetMkdirOption(mi *MkdirInfo) {
	fn(mi)
}

var _ MkdirOption = &MkdirInfo{}

func WithParents(b bool) MkdirOption {
	return mkdirOptionFunc(func(mi *MkdirInfo) {
		mi.MakeParents = b
	})
}

type MkdirInfo struct {
	MakeParents bool
	ChownOpt    *ChownOpt
}

func (mi *MkdirInfo) SetMkdirOption(mi2 *MkdirInfo) {
	*mi2 = *mi
}

func WithUser(name string) ChownOption {
	return ChownOpt{
		User: &UserOpt{Name: name},
	}
}

func WithUIDGID(uid, gid int) ChownOption {
	return ChownOpt{
		User:  &UserOpt{UID: uid},
		Group: &UserOpt{UID: gid},
	}
}

type ChownOpt struct {
	User  *UserOpt
	Group *UserOpt
}

func (co ChownOpt) SetMkdirOption(mi *MkdirInfo) {
	mi.ChownOpt = &co
}
func (co ChownOpt) SetMkfileOption(mi *MkfileInfo) {
	mi.ChownOpt = &co
}
func (co ChownOpt) SetCopyOption(mi *CopyInfo) {
	mi.ChownOpt = &co
}

func (cp *ChownOpt) marshal(base pb.InputIndex) *pb.ChownOpt {
	if cp == nil {
		return nil
	}
	return &pb.ChownOpt{
		User:  cp.User.marshal(base),
		Group: cp.Group.marshal(base),
	}
}

type UserOpt struct {
	UID  int
	Name string
}

func (up *UserOpt) marshal(base pb.InputIndex) *pb.UserOpt {
	if up == nil {
		return nil
	}
	if up.Name != "" {
		return &pb.UserOpt{Name: up.Name, Input: base}
	}
	return &pb.UserOpt{Id: int32(up.UID), Input: -1}
}

func Mkfile(p string, m os.FileMode, dt []byte, opts ...MkfileOption) *FileAction {
	var mi MkfileInfo
	for _, o := range opts {
		o.SetMkfileOption(&mi)
	}

	return &FileAction{
		action: &fileActionMkfile{
			file: p,
			mode: m,
			dt:   dt,
			info: mi,
		},
	}
}

type MkfileOption interface {
	SetMkfileOption(*MkfileInfo)
}

type MkfileInfo struct {
	ChownOpt *ChownOpt
}

func (mi *MkfileInfo) SetMkfileOption(mi2 *MkfileInfo) {
	*mi2 = *mi
}

var _ MkfileOption = &MkfileInfo{}

type fileActionMkfile struct {
	file string
	mode os.FileMode
	dt   []byte
	info MkfileInfo
}

func (a *fileActionMkfile) toProtoAction(parent string, base pb.InputIndex) pb.IsFileAction {
	return &pb.FileAction_Mkfile{
		Mkfile: &pb.FileActionMkFile{
			Path:  normalizePath(parent, a.file),
			Mode:  int32(a.mode & 0777),
			Data:  a.dt,
			Owner: a.info.ChownOpt.marshal(base),
		},
	}
}

func Rm(p string, opts ...RmOption) *FileAction {
	var mi RmInfo
	for _, o := range opts {
		o.SetRmOption(&mi)
	}

	return &FileAction{
		action: &fileActionRm{
			file: p,
			info: mi,
		},
	}
}

type RmOption interface {
	SetRmOption(*RmInfo)
}

type rmOptionFunc func(*RmInfo)

func (fn rmOptionFunc) SetRmOption(mi *RmInfo) {
	fn(mi)
}

type RmInfo struct {
	AllowNotFound bool
	AllowWildcard bool
}

func (mi *RmInfo) SetRmOption(mi2 *RmInfo) {
	*mi2 = *mi
}

var _ RmOption = &RmInfo{}

func WithAllowNotFound(b bool) RmOption {
	return rmOptionFunc(func(mi *RmInfo) {
		mi.AllowNotFound = b
	})
}

func WithAllowWildcard(b bool) RmOption {
	return rmOptionFunc(func(mi *RmInfo) {
		mi.AllowWildcard = b
	})
}

type fileActionRm struct {
	file string
	info RmInfo
}

func (a *fileActionRm) toProtoAction(parent string, base pb.InputIndex) pb.IsFileAction {
	return &pb.FileAction_Rm{
		Rm: &pb.FileActionRm{
			Path:          normalizePath(parent, a.file),
			AllowNotFound: a.info.AllowNotFound,
			AllowWildcard: a.info.AllowWildcard,
		},
	}
}

func Copy(input CopyInput, src, dest string, opts ...CopyOption) *FileAction {
	var state *State
	var fas *fileActionWithState
	var err error
	if st, ok := input.(State); ok {
		state = &st
	} else if v, ok := input.(*fileActionWithState); ok {
		fas = v
	} else {
		err = errors.Errorf("invalid input type %T for copy", input)
	}

	var mi CopyInfo
	for _, o := range opts {
		o.SetCopyOption(&mi)
	}

	return &FileAction{
		action: &fileActionCopy{
			state: state,
			fas:   fas,
			src:   src,
			dest:  dest,
			info:  mi,
		},
		err: err,
	}
}

type CopyOption interface {
	SetCopyOption(*CopyInfo)
}

type CopyInfo struct {
	Mode                *os.FileMode
	FollowSymlinks      bool
	CopyDirContentsOnly bool
	AttemptUnpack       bool
	CreateDestPath      bool
	AllowWildcard       bool
	AllowEmptyWildcard  bool
	ChownOpt            *ChownOpt
}

func (mi *CopyInfo) SetCopyOption(mi2 *CopyInfo) {
	*mi2 = *mi
}

var _ CopyOption = &CopyInfo{}

type fileActionCopy struct {
	state *State
	fas   *fileActionWithState
	src   string
	dest  string
	info  CopyInfo
}

func (a *fileActionCopy) toProtoAction(parent string, base pb.InputIndex) pb.IsFileAction {
	c := &pb.FileActionCopy{
		Src:                a.sourcePath(),
		Dest:               normalizePath(parent, a.dest),
		Owner:              a.info.ChownOpt.marshal(base),
		AllowWildcard:      a.info.AllowWildcard,
		AllowEmptyWildcard: a.info.AllowEmptyWildcard,
		FollowSymlink:      a.info.FollowSymlinks,
		DirCopyContents:    a.info.CopyDirContentsOnly,
		AttemptUnpack:      a.info.AttemptUnpack,
		CreateDestPath:     a.info.CreateDestPath,
	}
	if a.info.Mode != nil {
		c.Mode = int32(*a.info.Mode)
	} else {
		c.Mode = -1
	}
	return &pb.FileAction_Copy{
		Copy: c,
	}
}

func (c *fileActionCopy) sourcePath() string {
	p := path.Clean(c.src)
	if !path.IsAbs(p) {
		if c.state != nil {
			p = path.Join("/", c.state.GetDir(), p)
		} else if c.fas != nil {
			p = path.Join("/", c.fas.state.GetDir(), p)
		}
	}
	return p
}

type FileOp struct {
	MarshalCache
	action *FileAction
	output Output

	// constraints Constraints
	isValidated bool
}

func (f *FileOp) Validate() error {
	if f.isValidated {
		return nil
	}
	if f.action == nil {
		return errors.Errorf("action is required")
	}
	f.isValidated = true
	return nil
}

type marshalState struct {
	visited map[*FileAction]*fileActionState
	inputs  []*pb.Input
	actions []*fileActionState
}

func newMarshalState() *marshalState {
	return &marshalState{
		visited: map[*FileAction]*fileActionState{},
	}
}

type fileActionState struct {
	base           pb.InputIndex
	input          pb.InputIndex
	inputRelative  *int
	input2         pb.InputIndex
	input2Relative *int
	target         int
	action         subAction
	fa             *FileAction
}

func (ms *marshalState) addInput(st *fileActionState, c *Constraints, o Output) (pb.InputIndex, error) {
	inp, err := o.ToInput(c)
	if err != nil {
		return 0, err
	}
	for i, inp2 := range ms.inputs {
		if *inp == *inp2 {
			return pb.InputIndex(i), nil
		}
	}
	i := pb.InputIndex(len(ms.inputs))
	ms.inputs = append(ms.inputs, inp)
	return i, nil
}

func (ms *marshalState) add(fa *FileAction, c *Constraints) (*fileActionState, error) {
	if st, ok := ms.visited[fa]; ok {
		return st, nil
	}

	if fa.err != nil {
		return nil, fa.err
	}

	var prevState *fileActionState
	if parent := fa.prev; parent != nil {
		var err error
		prevState, err = ms.add(parent, c)
		if err != nil {
			return nil, err
		}
	}

	st := &fileActionState{
		action: fa.action,
		input:  -1,
		input2: -1,
		base:   -1,
		fa:     fa,
	}

	if source := fa.state.Output(); source != nil {
		inp, err := ms.addInput(st, c, source)
		if err != nil {
			return nil, err
		}
		st.base = inp
	}

	if fa.prev == nil {
		st.input = st.base
	} else {
		st.inputRelative = &prevState.target
	}

	if a, ok := fa.action.(*fileActionCopy); ok {
		if a.state != nil {
			if out := a.state.Output(); out != nil {
				inp, err := ms.addInput(st, c, out)
				if err != nil {
					return nil, err
				}
				st.input2 = inp
			}
		} else if a.fas != nil {
			src, err := ms.add(a.fas.FileAction, c)
			if err != nil {
				return nil, err
			}
			st.input2Relative = &src.target
		} else {
			return nil, errors.Errorf("invalid empty source for copy")
		}
	}

	st.target = len(ms.actions)

	ms.visited[fa] = st
	ms.actions = append(ms.actions, st)

	return st, nil
}

func (f *FileOp) Marshal(c *Constraints) (digest.Digest, []byte, *pb.OpMetadata, error) {
	if f.Cached(c) {
		return f.Load()
	}
	if err := f.Validate(); err != nil {
		return "", nil, nil, err
	}

	pfo := &pb.FileOp{}

	pop, md := MarshalConstraints(c, &Constraints{})
	pop.Op = &pb.Op_File{
		File: pfo,
	}

	state := newMarshalState()
	_, err := state.add(f.action, c)
	if err != nil {
		return "", nil, nil, err
	}
	pop.Inputs = state.inputs

	for i, st := range state.actions {
		output := pb.OutputIndex(-1)
		if i+1 == len(state.actions) {
			output = 0
		}

		var parent string
		if st.fa.state != nil {
			parent = st.fa.state.GetDir()
		}

		pfo.Actions = append(pfo.Actions, &pb.FileAction{
			Input:          getIndex(st.input, len(state.actions), st.inputRelative),
			SecondaryInput: getIndex(st.input2, len(state.actions), st.input2Relative),
			Output:         output,
			Action:         st.action.toProtoAction(parent, st.base),
		})
	}

	dt, err := pop.Marshal()
	if err != nil {
		return "", nil, nil, err
	}
	f.Store(dt, md, c)
	return f.Load()
}

func normalizePath(parent, p string) string {
	p = path.Clean(p)
	if !path.IsAbs(p) {
		p = path.Join("/", parent, p)
	}
	return p
}

func (f *FileOp) Output() Output {
	return f.output
}

func (f *FileOp) Inputs() (inputs []Output) {
	mm := map[Output]struct{}{}

	f.action.allOutputs(mm)

	for o := range mm {
		inputs = append(inputs, o)
	}
	return inputs
}

func getIndex(input pb.InputIndex, len int, relative *int) pb.InputIndex {
	if relative != nil {
		return pb.InputIndex(len + *relative)
	}
	return input
}
