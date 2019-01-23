package llb

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestFileMkdir(t *testing.T) {
	t.Parallel()

	st := Image("foo").File(Mkdir("/foo", 0700))
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 3, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[1])

	f := arr[1].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[1].Inputs), 1)
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))

	require.Equal(t, 1, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	mkdir := action.Action.(*pb.FileAction_Mkdir).Mkdir

	require.Equal(t, "/foo", mkdir.Path)
	require.Equal(t, 0700, int(mkdir.Mode))
}

func TestFileMkdirChain(t *testing.T) {
	t.Parallel()

	st := Image("foo").Dir("/etc").File(Mkdir("/foo", 0700).Mkdir("bar", 0600, WithParents(true)).Mkdir("bar/baz", 0701, WithParents(false)))
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 3, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[1])

	f := arr[1].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[1].Inputs), 1)
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))

	require.Equal(t, 3, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))
	mkdir := action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Equal(t, "/foo", mkdir.Path)
	require.Equal(t, 0700, int(mkdir.Mode))
	require.Equal(t, false, mkdir.MakeParents)
	require.Nil(t, mkdir.Owner)

	action = f.Actions[1]
	require.Equal(t, 3, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))
	mkdir = action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Equal(t, "/etc/bar", mkdir.Path)
	require.Equal(t, 0600, int(mkdir.Mode))
	require.Equal(t, true, mkdir.MakeParents)
	require.Nil(t, mkdir.Owner)

	action = f.Actions[2]
	require.Equal(t, 4, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))
	mkdir = action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Equal(t, "/etc/bar/baz", mkdir.Path)
	require.Equal(t, 0701, int(mkdir.Mode))
	require.Equal(t, false, mkdir.MakeParents)
	require.Nil(t, mkdir.Owner)
}

func TestFileMkfile(t *testing.T) {
	t.Parallel()

	st := Image("foo").File(Mkfile("/foo", 0700, []byte("data")))
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 3, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[1])

	f := arr[1].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[1].Inputs), 1)
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))

	require.Equal(t, 1, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	mkdir := action.Action.(*pb.FileAction_Mkfile).Mkfile

	require.Equal(t, "/foo", mkdir.Path)
	require.Equal(t, 0700, int(mkdir.Mode))
	require.Equal(t, "data", string(mkdir.Data))
}

func TestFileRm(t *testing.T) {
	t.Parallel()

	st := Image("foo").File(Rm("/foo"))
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 3, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[1])

	f := arr[1].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[1].Inputs), 1)
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))

	require.Equal(t, 1, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	rm := action.Action.(*pb.FileAction_Rm).Rm
	require.Equal(t, "/foo", rm.Path)
}

func TestFileSimpleChains(t *testing.T) {
	t.Parallel()

	st := Image("foo").Dir("/tmp").
		File(
			Mkdir("foo/bar/", 0700).
				Rm("abc").
				Mkfile("foo/bar/baz", 0777, []byte("d0")),
		).
		Dir("sub").
		File(
			Rm("foo").
				Mkfile("/abc", 0701, []byte("d1")),
		)
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 4, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[2])

	f := arr[2].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[2].Inputs), 1)
	require.Equal(t, m[arr[2].Inputs[0].Digest], arr[1])
	require.Equal(t, 0, int(arr[2].Inputs[0].Index))
	require.Equal(t, 2, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	rm := action.Action.(*pb.FileAction_Rm).Rm
	require.Equal(t, "/tmp/sub/foo", rm.Path)

	action = f.Actions[1]
	require.Equal(t, 2, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	mkfile := action.Action.(*pb.FileAction_Mkfile).Mkfile
	require.Equal(t, "/abc", mkfile.Path)

	f = arr[1].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[1].Inputs), 1)
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))
	require.Equal(t, 3, len(f.Actions))

	action = f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	mkdir := action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Equal(t, "/tmp/foo/bar", mkdir.Path)

	action = f.Actions[1]
	require.Equal(t, 3, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	rm = action.Action.(*pb.FileAction_Rm).Rm
	require.Equal(t, "/tmp/abc", rm.Path)

	action = f.Actions[2]
	require.Equal(t, 4, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	mkfile = action.Action.(*pb.FileAction_Mkfile).Mkfile
	require.Equal(t, "/tmp/foo/bar/baz", mkfile.Path)
}

func TestFileCopy(t *testing.T) {
	t.Parallel()

	st := Image("foo").Dir("/tmp").File(Copy(Image("bar").Dir("/etc"), "foo", "bar"))
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 4, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[2])

	f := arr[2].Op.(*pb.Op_File).File
	require.Equal(t, 2, len(arr[2].Inputs))
	require.Equal(t, "docker-image://docker.io/library/foo:latest", m[arr[2].Inputs[0].Digest].Op.(*pb.Op_Source).Source.Identifier)
	require.Equal(t, 0, int(arr[2].Inputs[0].Index))
	require.Equal(t, "docker-image://docker.io/library/bar:latest", m[arr[2].Inputs[1].Digest].Op.(*pb.Op_Source).Source.Identifier)
	require.Equal(t, 0, int(arr[2].Inputs[1].Index))

	require.Equal(t, 1, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, 1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	copy := action.Action.(*pb.FileAction_Copy).Copy

	require.Equal(t, "/etc/foo", copy.Src)
	require.Equal(t, "/tmp/bar", copy.Dest)
}

func TestFileCopyFromAction(t *testing.T) {
	t.Parallel()

	st := Image("foo").Dir("/out").File(
		Copy(
			Mkdir("foo", 0700).
				Mkfile("foo/bar", 0600, []byte("dt")).
				WithState(Scratch().Dir("/tmp")),
			"foo/bar", "baz"))
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 3, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[1])

	f := arr[1].Op.(*pb.Op_File).File
	require.Equal(t, 1, len(arr[1].Inputs))
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))

	require.Equal(t, 3, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, -1, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	mkdir := action.Action.(*pb.FileAction_Mkdir).Mkdir

	require.Equal(t, "/tmp/foo", mkdir.Path)
	require.Equal(t, 0700, int(mkdir.Mode))

	action = f.Actions[1]
	require.Equal(t, 3, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	mkfile := action.Action.(*pb.FileAction_Mkfile).Mkfile

	require.Equal(t, "/tmp/foo/bar", mkfile.Path)
	require.Equal(t, 0600, int(mkfile.Mode))
	require.Equal(t, "dt", string(mkfile.Data))

	action = f.Actions[2]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, 4, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	copy := action.Action.(*pb.FileAction_Copy).Copy

	require.Equal(t, "/tmp/foo/bar", copy.Src)
	require.Equal(t, "/out/baz", copy.Dest)
}

func TestFilePipeline(t *testing.T) {
	t.Parallel()

	st := Image("foo").Dir("/out").
		File(
			Copy(
				Mkdir("foo", 0700).
					Mkfile("foo/bar", 0600, []byte("dt")).
					WithState(Image("bar").Dir("/tmp")),
				"foo/bar", "baz").
				Rm("foo/bax"),
		).
		File(
			Mkdir("/bar", 0701).
				Copy(Image("foo"), "in", "out").
				Copy(Image("baz").Dir("/base"), "in2", "out2"),
		)
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)

	require.Equal(t, 6, len(arr)) // 3 img + 2 file + pointer

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[4])

	f := arr[4].Op.(*pb.Op_File).File
	require.Equal(t, 3, len(arr[4].Inputs))

	require.Equal(t, "docker-image://docker.io/library/foo:latest", m[arr[4].Inputs[1].Digest].Op.(*pb.Op_Source).Source.Identifier)
	require.Equal(t, 0, int(arr[4].Inputs[1].Index))
	require.Equal(t, "docker-image://docker.io/library/baz:latest", m[arr[4].Inputs[2].Digest].Op.(*pb.Op_Source).Source.Identifier)
	require.Equal(t, 0, int(arr[4].Inputs[2].Index))

	require.Equal(t, 3, len(f.Actions))

	action := f.Actions[0]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	mkdir := action.Action.(*pb.FileAction_Mkdir).Mkdir

	require.Equal(t, "/bar", mkdir.Path)
	require.Equal(t, 0701, int(mkdir.Mode))

	action = f.Actions[1]
	require.Equal(t, 3, int(action.Input))
	require.Equal(t, 1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	copy := action.Action.(*pb.FileAction_Copy).Copy

	require.Equal(t, "/in", copy.Src)
	require.Equal(t, "/out/out", copy.Dest)

	action = f.Actions[2]
	require.Equal(t, 4, int(action.Input))
	require.Equal(t, 2, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	copy = action.Action.(*pb.FileAction_Copy).Copy

	require.Equal(t, "/base/in2", copy.Src)
	require.Equal(t, "/out/out2", copy.Dest)

	f = m[arr[4].Inputs[0].Digest].Op.(*pb.Op_File).File
	op := m[arr[4].Inputs[0].Digest]
	require.Equal(t, 2, len(op.Inputs))
	require.Equal(t, 4, len(f.Actions))

	action = f.Actions[0]
	require.Equal(t, 1, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))
	require.Equal(t, "docker-image://docker.io/library/bar:latest", m[op.Inputs[1].Digest].Op.(*pb.Op_Source).Source.Identifier)
	mkdir = action.Action.(*pb.FileAction_Mkdir).Mkdir

	require.Equal(t, "/tmp/foo", mkdir.Path)
	require.Equal(t, 0700, int(mkdir.Mode))

	action = f.Actions[1]
	require.Equal(t, 4, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))

	mkfile := action.Action.(*pb.FileAction_Mkfile).Mkfile

	require.Equal(t, "/tmp/foo/bar", mkfile.Path)
	require.Equal(t, 0600, int(mkfile.Mode))
	require.Equal(t, "dt", string(mkfile.Data))

	action = f.Actions[2]
	require.Equal(t, 0, int(action.Input))
	require.Equal(t, 5, int(action.SecondaryInput))
	require.Equal(t, -1, int(action.Output))
	require.Equal(t, arr[4].Inputs[1].Digest, op.Inputs[0].Digest)

	copy = action.Action.(*pb.FileAction_Copy).Copy

	require.Equal(t, "/tmp/foo/bar", copy.Src)
	require.Equal(t, "/out/baz", copy.Dest)

	action = f.Actions[3]
	require.Equal(t, 6, int(action.Input))
	require.Equal(t, -1, int(action.SecondaryInput))
	require.Equal(t, 0, int(action.Output))

	rm := action.Action.(*pb.FileAction_Rm).Rm
	require.Equal(t, "/out/foo/bax", rm.Path)
}

func TestFileOwner(t *testing.T) {
	t.Parallel()

	st := Image("foo").File(Mkdir("/foo", 0700).Mkdir("bar", 0600, WithUIDGID(123, 456)).Mkdir("bar/baz", 0701, WithUser("foouser")))
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 3, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[1])

	f := arr[1].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[1].Inputs), 1)
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))

	require.Equal(t, 3, len(f.Actions))

	action := f.Actions[0]
	mkdir := action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Nil(t, mkdir.Owner)

	action = f.Actions[1]
	mkdir = action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Equal(t, 123, int(mkdir.Owner.User.Id))
	require.Equal(t, "", mkdir.Owner.User.Name)
	require.Equal(t, -1, int(mkdir.Owner.User.Input))
	require.Equal(t, 456, int(mkdir.Owner.Group.Id))
	require.Equal(t, "", mkdir.Owner.Group.Name)
	require.Equal(t, -1, int(mkdir.Owner.Group.Input))

	action = f.Actions[2]
	mkdir = action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Equal(t, 0, int(mkdir.Owner.User.Id))
	require.Equal(t, "foouser", mkdir.Owner.User.Name)
	require.Equal(t, 0, int(mkdir.Owner.User.Input))
	require.Nil(t, mkdir.Owner.Group)
}

func TestFileCopyOwner(t *testing.T) {
	t.Parallel()

	st := Scratch().
		File(Mkdir("/foo", 0700, WithUser("user1")).
			Copy(Image("foo"), "src1", "dst", WithUser("user2")).
			Copy(
				Copy(Scratch(), "src0", "src2", WithUser("user3")).WithState(Image("foo")),
				"src2", "dst", WithUser("user4")).
			Copy(Image("foo"), "src3", "dst", WithUIDGID(1, 2)),
		)
	def, err := st.Marshal()

	require.NoError(t, err)

	m, arr := parseDef(t, def.Def)
	require.Equal(t, 3, len(arr))

	dgst, idx := last(t, arr)
	require.Equal(t, 0, idx)
	require.Equal(t, m[dgst], arr[1])

	f := arr[1].Op.(*pb.Op_File).File
	require.Equal(t, len(arr[1].Inputs), 1)
	require.Equal(t, m[arr[1].Inputs[0].Digest], arr[0])
	require.Equal(t, 0, int(arr[1].Inputs[0].Index))

	require.Equal(t, 5, len(f.Actions))

	action := f.Actions[0]
	mkdir := action.Action.(*pb.FileAction_Mkdir).Mkdir
	require.Equal(t, 0, int(mkdir.Owner.User.Id))
	require.Equal(t, "user1", mkdir.Owner.User.Name)
	require.Equal(t, -1, int(mkdir.Owner.User.Input))
	require.Nil(t, mkdir.Owner.Group)

	action = f.Actions[1]
	copy := action.Action.(*pb.FileAction_Copy).Copy
	require.Equal(t, "/src1", copy.Src)
	require.Equal(t, 0, int(copy.Owner.User.Id))
	require.Equal(t, "user2", copy.Owner.User.Name)
	require.Equal(t, -1, int(copy.Owner.User.Input))
	require.Nil(t, copy.Owner.Group)

	action = f.Actions[2]
	copy = action.Action.(*pb.FileAction_Copy).Copy
	require.Equal(t, "/src0", copy.Src)
	require.Equal(t, 0, int(copy.Owner.User.Id))
	require.Equal(t, "user3", copy.Owner.User.Name)
	require.Equal(t, 0, int(copy.Owner.User.Input))
	require.Nil(t, copy.Owner.Group)

	action = f.Actions[3]
	copy = action.Action.(*pb.FileAction_Copy).Copy
	require.Equal(t, "/src2", copy.Src)
	require.Equal(t, 0, int(copy.Owner.User.Id))
	require.Equal(t, "user4", copy.Owner.User.Name)
	require.Equal(t, -1, int(copy.Owner.User.Input))
	require.Nil(t, copy.Owner.Group)

	action = f.Actions[4]
	copy = action.Action.(*pb.FileAction_Copy).Copy
	require.Equal(t, "/src3", copy.Src)
	require.Equal(t, 1, int(copy.Owner.User.Id))
	require.Equal(t, "", copy.Owner.User.Name)
	require.Equal(t, -1, int(copy.Owner.User.Input))
	require.Equal(t, 2, int(copy.Owner.Group.Id))
	require.Equal(t, "", copy.Owner.Group.Name)
	require.Equal(t, -1, int(copy.Owner.Group.Input))
}

func parseDef(t *testing.T, def [][]byte) (map[digest.Digest]pb.Op, []pb.Op) {
	m := map[digest.Digest]pb.Op{}
	arr := make([]pb.Op, 0, len(def))

	for _, dt := range def {
		var op pb.Op
		err := (&op).Unmarshal(dt)
		require.NoError(t, err)
		dgst := digest.FromBytes(dt)
		m[dgst] = op
		arr = append(arr, op)
		// fmt.Printf(":: %T %+v\n", op.Op, op)
	}

	return m, arr
}

func last(t *testing.T, arr []pb.Op) (digest.Digest, int) {
	require.True(t, len(arr) > 1)

	op := arr[len(arr)-1]
	require.Equal(t, 1, len(op.Inputs))
	return op.Inputs[0].Digest, int(op.Inputs[0].Index)
}
