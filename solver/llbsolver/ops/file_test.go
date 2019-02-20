package ops

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestMkdirMkfile(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         -1,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
			{
				Input:          1,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkfile{
					Mkfile: &pb.FileActionMkFile{
						Path: "/foo/bar/baz",
						Mode: 0700,
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	inp := rb.NewRef("ref1")
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{inp}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 1)
	rb.checkReleased(t, append(outs, inp))

	o := outs[0].(*testFileRef)
	require.Equal(t, "mount-ref1-mkdir-mkfile-commit", o.id)
	require.Equal(t, 2, len(o.mount.chain))
	require.Equal(t, fo.Actions[0].Action.(*pb.FileAction_Mkdir).Mkdir, o.mount.chain[0].mkdir)
	require.Equal(t, fo.Actions[1].Action.(*pb.FileAction_Mkfile).Mkfile, o.mount.chain[1].mkfile)
}

func TestInvalidNoOutput(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         -1,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	rb.checkReleased(t, outs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no outputs specified")
}

func TestInvalidDuplicateOutput(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
			{
				Input:          1,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkfile{
					Mkfile: &pb.FileActionMkFile{
						Path: "/foo/bar/baz",
						Mode: 0700,
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	_, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate output")
	rb.checkReleased(t, nil)
}

func TestActionInvalidIndex(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	_, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "loop from index")
	rb.checkReleased(t, nil)
}

func TestActionLoop(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          1,
				SecondaryInput: -1,
				Output:         -1,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkfile{
					Mkfile: &pb.FileActionMkFile{
						Path: "/foo/bar/baz",
						Mode: 0700,
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	_, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "loop from index")
	rb.checkReleased(t, nil)
}

func TestMultiOutput(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
			{
				Input:          1,
				SecondaryInput: -1,
				Output:         1,
				Action: &pb.FileAction_Mkfile{
					Mkfile: &pb.FileActionMkFile{
						Path: "/foo/bar/baz",
						Mode: 0700,
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	inp := rb.NewRef("ref1")
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{inp}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 2)
	rb.checkReleased(t, append(outs, inp))

	o := outs[0].(*testFileRef)
	require.Equal(t, "mount-ref1-mkdir-commit", o.id)
	require.Equal(t, 1, len(o.mount.chain))
	require.Equal(t, fo.Actions[0].Action.(*pb.FileAction_Mkdir).Mkdir, o.mount.chain[0].mkdir)

	o = outs[1].(*testFileRef)
	require.Equal(t, "mount-ref1-mkdir-mkfile-commit", o.id)
	require.Equal(t, 2, len(o.mount.chain))
	require.Equal(t, fo.Actions[0].Action.(*pb.FileAction_Mkdir).Mkdir, o.mount.chain[0].mkdir)
	require.Equal(t, fo.Actions[1].Action.(*pb.FileAction_Mkfile).Mkfile, o.mount.chain[1].mkfile)
}

func TestFileFromScratch(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          -1,
				SecondaryInput: -1,
				Output:         -1,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkfile{
					Mkfile: &pb.FileActionMkFile{
						Path: "/foo/bar/baz",
						Mode: 0700,
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 1)
	rb.checkReleased(t, outs)

	o := outs[0].(*testFileRef)

	require.Equal(t, "mount-scratch-mkdir-mkfile-commit", o.id)
	require.Equal(t, 2, len(o.mount.chain))
	require.Equal(t, fo.Actions[0].Action.(*pb.FileAction_Mkdir).Mkdir, o.mount.chain[0].mkdir)
	require.Equal(t, fo.Actions[1].Action.(*pb.FileAction_Mkfile).Mkfile, o.mount.chain[1].mkfile)
}

func TestFileCopyInputRm(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         -1,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
						Mode:        0700,
					},
				},
			},
			{
				Input:          1,
				SecondaryInput: 2,
				Output:         -1,
				Action: &pb.FileAction_Copy{
					Copy: &pb.FileActionCopy{
						Src:  "/src",
						Dest: "/dest",
					},
				},
			},
			{
				Input:          3,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Rm{
					Rm: &pb.FileActionRm{
						Path: "/foo/bar/baz",
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	inp0 := rb.NewRef("srcref")
	inp1 := rb.NewRef("destref")
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{inp0, inp1}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 1)
	rb.checkReleased(t, append(outs, inp0, inp1))

	o := outs[0].(*testFileRef)
	require.Equal(t, "mount-destref-copy(mount-srcref-mkdir)-rm-commit", o.id)
	require.Equal(t, 2, len(o.mount.chain))
	require.Equal(t, fo.Actions[0].Action.(*pb.FileAction_Mkdir).Mkdir, o.mount.chain[0].copySrc[0].mkdir)
	require.Equal(t, fo.Actions[1].Action.(*pb.FileAction_Copy).Copy, o.mount.chain[0].copy)
	require.Equal(t, fo.Actions[2].Action.(*pb.FileAction_Rm).Rm, o.mount.chain[1].rm)
}

func TestFileParallelActions(t *testing.T) {
	// two mkdirs from scratch copied over each other. mkdirs should happen in parallel
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         -1,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path: "/foo",
					},
				},
			},
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         -1,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path: "/bar",
					},
				},
			},
			{
				Input:          2,
				SecondaryInput: 1,
				Output:         0,
				Action: &pb.FileAction_Copy{
					Copy: &pb.FileActionCopy{
						Src:  "/src",
						Dest: "/dest",
					},
				},
			},
		},
	}

	s, rb := newTestFileSolver()
	inp := rb.NewRef("inpref")

	ch := make(chan struct{})
	var sem int64
	inp.callback = func() {
		if atomic.AddInt64(&sem, 1) == 2 {
			close(ch)
		}
		<-ch
	}

	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{inp}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 1)

	require.Equal(t, int64(2), sem)
}

func newTestFileSolver() (*FileOpSolver, *testFileRefBackend) {
	trb := &testFileRefBackend{refs: map[*testFileRef]struct{}{}, mounts: map[string]*testMount{}}
	return NewFileOpSolver(&testFileBackend{}, trb), trb
}

type testFileRef struct {
	id       string
	mount    *testMount
	refcount int
	callback func()
}

func (r *testFileRef) Release(context.Context) error {
	if r.refcount == 0 {
		return errors.Errorf("ref already released")
	}
	r.refcount--
	return nil
}

type testMount struct {
	b         *testFileRefBackend
	id        string
	initID    string
	chain     []mod
	callback  func()
	unmounted bool
	active    *testFileRef
}

type mod struct {
	mkdir   *pb.FileActionMkDir
	rm      *pb.FileActionRm
	mkfile  *pb.FileActionMkFile
	copy    *pb.FileActionCopy
	copySrc []mod
}

func (m *testMount) IsFileOpMount() {}
func (m *testMount) Release(ctx context.Context) error {
	if m.initID != m.id {
		return m.b.mounts[m.initID].Release(ctx)
	}
	if m.unmounted {
		return errors.Errorf("already unmounted")
	}
	m.unmounted = true
	if m.active != nil {
		return m.active.Release(ctx)
	}
	return nil
}

type testFileBackend struct {
}

func (b *testFileBackend) Mkdir(_ context.Context, m fileoptypes.Mount, a pb.FileActionMkDir) error {
	mm := m.(*testMount)
	if mm.callback != nil {
		mm.callback()
	}
	mm.id += "-mkdir"
	mm.chain = append(mm.chain, mod{mkdir: &a})
	return nil
}

func (b *testFileBackend) Mkfile(_ context.Context, m fileoptypes.Mount, a pb.FileActionMkFile) error {
	mm := m.(*testMount)
	mm.id += "-mkfile"
	mm.chain = append(mm.chain, mod{mkfile: &a})
	return nil
}
func (b *testFileBackend) Rm(_ context.Context, m fileoptypes.Mount, a pb.FileActionRm) error {
	mm := m.(*testMount)
	mm.id += "-rm"
	mm.chain = append(mm.chain, mod{rm: &a})
	return nil
}
func (b *testFileBackend) Copy(_ context.Context, m1 fileoptypes.Mount, m fileoptypes.Mount, a pb.FileActionCopy) error {
	mm := m.(*testMount)
	mm1 := m1.(*testMount)
	mm.id += "-copy(" + mm1.id + ")"
	mm.chain = append(mm.chain, mod{copy: &a, copySrc: mm1.chain})
	return nil
}

type testFileRefBackend struct {
	refs   map[*testFileRef]struct{}
	mounts map[string]*testMount
}

func (b *testFileRefBackend) NewRef(id string) *testFileRef {
	r := &testFileRef{refcount: 1, id: id}
	b.refs[r] = struct{}{}
	return r
}

func (b *testFileRefBackend) Prepare(ctx context.Context, ref fileoptypes.Ref, readonly bool) (fileoptypes.Mount, error) {
	var active *testFileRef
	if ref == nil {
		active = b.NewRef("scratch")
		ref = active
	}
	rr := ref.(*testFileRef)
	m := rr.mount
	if m == nil {
		m = &testMount{b: b, id: "mount-" + rr.id, callback: rr.callback}
	}
	m.initID = m.id
	m.active = active
	b.mounts[m.initID] = m
	m2 := *m
	m2.chain = append([]mod{}, m2.chain...)
	return &m2, nil
}
func (b *testFileRefBackend) Commit(ctx context.Context, mount fileoptypes.Mount) (fileoptypes.Ref, error) {
	m := mount.(*testMount)
	if err := b.mounts[m.initID].Release(context.TODO()); err != nil {
		return nil, err
	}
	m2 := *m
	m2.unmounted = false
	m2.callback = nil
	r := b.NewRef(m2.id + "-commit")
	r.mount = &m2
	return r, nil
}

func (b *testFileRefBackend) checkReleased(t *testing.T, outs []fileoptypes.Ref) {
loop0:
	for r := range b.refs {
		for _, o := range outs {
			if o.(*testFileRef) == r {
				require.Equal(t, 1, r.refcount)
				continue loop0
			}
		}
		require.Equal(t, 0, r.refcount, "%s not released", r.id)
	}
	for _, o := range outs {
		_, ok := b.refs[o.(*testFileRef)]
		require.True(t, ok)
	}

	for _, m := range b.mounts {
		require.True(t, m.unmounted, "%s still mounted", m.id)
	}
}
