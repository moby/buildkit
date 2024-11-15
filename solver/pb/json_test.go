package pb

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSON_Op(t *testing.T) {
	for _, tt := range []struct {
		name string
		op   *Op
		json string
	}{
		{
			name: "exec",
			op: &Op{
				Op: &Op_Exec{
					Exec: &ExecOp{
						Meta: &Meta{
							Args: []string{"echo", "Hello", "World"},
						},
						Mounts: []*Mount{
							{Input: 0, Dest: "/", Readonly: true},
						},
					},
				},
			},
			json: `{"Op":{"exec":{"meta":{"args":["echo","Hello","World"]},"mounts":[{"dest":"/","readonly":true}]}}}`,
		},
		{
			name: "source",
			op: &Op{
				Op: &Op_Source{
					Source: &SourceOp{
						Identifier: "local://context",
					},
				},
				Constraints: &WorkerConstraints{},
			},
			json: `{"Op":{"source":{"identifier":"local://context"}},"constraints":{}}`,
		},
		{
			name: "file",
			op: &Op{
				Op: &Op_File{
					File: &FileOp{
						Actions: []*FileAction{
							{
								Input:  1,
								Output: 2,
								Action: &FileAction_Copy{
									Copy: &FileActionCopy{
										Src:  "/foo",
										Dest: "/bar",
									},
								},
							},
						},
					},
				},
			},
			json: `{"Op":{"file":{"actions":[{"Action":{"copy":{"src":"/foo","dest":"/bar"}},"input":1,"secondaryInput":0,"output":2}]}}}`,
		},
		{
			name: "build",
			op: &Op{
				Op: &Op_Build{
					Build: &BuildOp{
						Def: &Definition{},
					},
				},
			},
			json: `{"Op":{"build":{"def":{}}}}`,
		},
		{
			name: "merge",
			op: &Op{
				Op: &Op_Merge{
					Merge: &MergeOp{
						Inputs: []*MergeInput{
							{Input: 0},
							{Input: 1},
						},
					},
				},
			},
			json: `{"Op":{"merge":{"inputs":[{}, {"input":1}]}}}`,
		},
		{
			name: "diff",
			op: &Op{
				Op: &Op_Diff{
					Diff: &DiffOp{
						Lower: &LowerDiffInput{Input: 0},
						Upper: &UpperDiffInput{Input: 1},
					},
				},
			},
			json: `{"Op":{"diff":{"lower":{},"upper":{"input":1}}}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal the operation.
			out, err := json.Marshal(tt.op)
			require.NoError(t, err)

			// Test that we received the correct json object.
			t.Run("Marshal", func(t *testing.T) {
				var act any
				err := json.Unmarshal(out, &act)
				require.NoError(t, err)

				var exp any
				err = json.Unmarshal([]byte(tt.json), &exp)
				require.NoError(t, err)
				require.Equal(t, exp, act)
			})

			// Verify that unmarshaling the same JSON results in the
			// same operation.
			t.Run("Unmarshal", func(t *testing.T) {
				exp, got := tt.op, &Op{}
				err := json.Unmarshal(out, got)
				require.NoError(t, err)
				require.Equal(t, exp, got)
			})
		})
	}
}

func TestJSON_FileAction(t *testing.T) {
	for _, tt := range []struct {
		name       string
		fileAction *FileAction
		json       string
	}{
		{
			name: "copy",
			fileAction: &FileAction{
				Action: &FileAction_Copy{
					Copy: &FileActionCopy{
						Src:  "/foo",
						Dest: "/bar",
					},
				},
			},
			json: `{"Action":{"copy":{"src":"/foo","dest":"/bar"}},"input":0,"secondaryInput":0,"output":0}`,
		},
		{
			name: "mkfile",
			fileAction: &FileAction{
				Action: &FileAction_Mkfile{
					Mkfile: &FileActionMkFile{
						Path: "/foo",
						Data: []byte("Hello, World!"),
					},
				},
			},
			json: `{"Action":{"mkfile":{"path":"/foo","data":"SGVsbG8sIFdvcmxkIQ=="}},"input":0,"secondaryInput":0,"output":0}`,
		},
		{
			name: "mkdir",
			fileAction: &FileAction{
				Action: &FileAction_Mkdir{
					Mkdir: &FileActionMkDir{
						Path:        "/foo/bar",
						MakeParents: true,
					},
				},
			},
			json: `{"Action":{"mkdir":{"path":"/foo/bar","makeParents":true}},"input":0,"secondaryInput":0,"output":0}`,
		},
		{
			name: "rm",
			fileAction: &FileAction{
				Action: &FileAction_Rm{
					Rm: &FileActionRm{
						Path:          "/foo",
						AllowNotFound: true,
					},
				},
			},
			json: `{"Action":{"rm":{"path":"/foo","allowNotFound":true}},"input":0,"secondaryInput":0,"output":0}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := json.Marshal(tt.fileAction)
			require.NoError(t, err)

			// Test that we received the correct json object.
			t.Run("Marshal", func(t *testing.T) {
				var act any
				err := json.Unmarshal(out, &act)
				require.NoError(t, err)

				var exp any
				err = json.Unmarshal([]byte(tt.json), &exp)
				require.NoError(t, err)
				require.Equal(t, exp, act)
			})

			// Verify that unmarshaling the same JSON results in the
			// same file action.
			t.Run("Unmarshal", func(t *testing.T) {
				exp, got := tt.fileAction, &FileAction{}
				err := json.Unmarshal(out, got)
				require.NoError(t, err)
				require.Equal(t, exp, got)
			})
		})
	}
}

func TestJSON_UserOpt(t *testing.T) {
	for _, tt := range []struct {
		name    string
		userOpt *UserOpt
		json    string
	}{
		{
			name: "byName",
			userOpt: &UserOpt{
				User: &UserOpt_ByName{
					ByName: &NamedUserOpt{
						Name: "foo",
					},
				},
			},
			json: `{"User":{"byName":{"name":"foo"}}}`,
		},
		{
			name: "byId",
			userOpt: &UserOpt{
				User: &UserOpt_ByID{
					ByID: 2,
				},
			},
			json: `{"User":{"byId":2}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := json.Marshal(tt.userOpt)
			require.NoError(t, err)

			// Test that we received the correct json object.
			t.Run("Marshal", func(t *testing.T) {
				var act any
				err := json.Unmarshal(out, &act)
				require.NoError(t, err)

				var exp any
				err = json.Unmarshal([]byte(tt.json), &exp)
				require.NoError(t, err)
				require.Equal(t, exp, act)
			})

			// Verify that unmarshaling the same JSON results in the
			// same user option.
			t.Run("Unmarshal", func(t *testing.T) {
				exp, got := tt.userOpt, &UserOpt{}
				err := json.Unmarshal(out, got)
				require.NoError(t, err)
				require.Equal(t, exp, got)
			})
		})
	}
}
