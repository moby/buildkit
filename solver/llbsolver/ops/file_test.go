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

	s := newTestFileSolver()
	inp := newTestRef("ref1")
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{inp}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 1)

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

	s := newTestFileSolver()
	_, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
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

	s := newTestFileSolver()
	_, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate output")
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

	s := newTestFileSolver()
	_, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "loop from index")
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

	s := newTestFileSolver()
	_, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.Error(t, err)
	require.Contains(t, err.Error(), "loop from index")
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

	s := newTestFileSolver()
	inp := newTestRef("ref1")
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{inp}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 2)

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

	s := newTestFileSolver()
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 1)

	o := outs[0].(*testFileRef)

	require.Equal(t, "scratch-mkdir-mkfile-commit", o.id)
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

	s := newTestFileSolver()
	inp0 := newTestRef("srcref")
	inp1 := newTestRef("destref")
	outs, err := s.Solve(context.TODO(), []fileoptypes.Ref{inp0, inp1}, fo.Actions)
	require.NoError(t, err)
	require.Equal(t, len(outs), 1)

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

	s := newTestFileSolver()
	inp := newTestRef("inpref")

	ch := make(chan struct{})
	var sem int64
	inp.mount.callback = func() {
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

func newTestFileSolver() *FileOpSolver {
	return NewFileOpSolver(&testFileBackend{}, &testFileRefBackend{})
}

type testFileRef struct {
	id       string
	mount    testMount
	released bool
}

func (r *testFileRef) Release(context.Context) error {
	if r.released {
		return errors.Errorf("ref already released")
	}
	r.released = true
	return nil
}

func newTestRef(id string) *testFileRef {
	return &testFileRef{mount: testMount{id: "mount-" + id}, id: id}
}

type testMount struct {
	id       string
	released bool
	chain    []mod
	callback func()
}

type mod struct {
	mkdir   *pb.FileActionMkDir
	rm      *pb.FileActionRm
	mkfile  *pb.FileActionMkFile
	copy    *pb.FileActionCopy
	copySrc []mod
}

func (m *testMount) Release(context.Context) error {
	if m.released {
		return errors.Errorf("already released")
	}
	m.released = true
	return nil
}

func (m *testMount) IsFileOpMount() {}

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
}

func (b *testFileRefBackend) Prepare(ctx context.Context, ref fileoptypes.Ref, readonly bool) (fileoptypes.Mount, error) {
	if ref == nil {
		return &testMount{id: "scratch"}, nil
	}
	m := ref.(*testFileRef).mount
	m.chain = append([]mod{}, m.chain...)
	return &m, nil
}
func (b *testFileRefBackend) Commit(ctx context.Context, mount fileoptypes.Mount) (fileoptypes.Ref, error) {
	m := *mount.(*testMount)
	return &testFileRef{mount: m, id: m.id + "-commit"}, nil
}
