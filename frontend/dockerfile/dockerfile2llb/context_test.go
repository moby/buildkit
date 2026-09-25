package dockerfile2llb

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerui"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	gwpb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestUnusedLocalContext(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, dockerfile string
	}{
		{"scratch", "FROM scratch\nENV FOO=bar\n"},
		{"run", "FROM scratch\nRUN echo hello\n"},
		{"heredoc", "FROM scratch\nCOPY <<EOF /hello\nhello\nEOF\n"},
		{"http", "FROM scratch\nADD https://example.com/file /file\n"},
		{"git", "FROM scratch\nADD https://github.com/moby/buildkit.git /src\n"},
		{"unused-stage", "FROM scratch AS unused\nCOPY . /src\nFROM scratch\n"},
		{"copy-from", "FROM scratch AS base\nCOPY <<EOF /hello\nhello\nEOF\nFROM scratch\nCOPY --from=base /hello /hello\n"},
		{"mount-from", "FROM scratch AS base\nCOPY <<EOF /hello\nhello\nEOF\nFROM scratch\nRUN --mount=from=base,target=/src echo hello\n"},
		{"named-base", "FROM assets\n"},
		{"named-copy", "FROM scratch\nCOPY --from=assets /hello /hello\n"},
		{"named-mount", "FROM scratch\nRUN --mount=from=assets,target=/src echo hello\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &contextClient{opts: map[string]string{"context:assets": "docker-image://scratch"}}
			bc, err := dockerui.NewClient(c)
			require.NoError(t, err)
			res, err := Dockerfile2LLB(t.Context(), []byte(tc.dockerfile), ConvertOpt{Client: bc})
			require.NoError(t, err)
			def, err := res.State.Marshal(t.Context())
			require.NoError(t, err)
			require.Zero(t, c.solves, "unused local context must not be solved")
			for _, dt := range def.Def {
				var op pb.Op
				require.NoError(t, op.Unmarshal(dt))
				if src := op.GetSource(); src != nil {
					require.NotEqual(t, "local://context", src.Identifier)
				}
			}
		})
	}
}

func TestLocalContextDockerignore(t *testing.T) {
	t.Parallel()
	for _, instruction := range []string{"COPY", "ADD"} {
		for _, tc := range []struct {
			name, patterns string
			wantWarning    bool
			wantError      string
		}{
			{name: "missing"},
			{name: "ignored", patterns: "foo\n", wantWarning: true},
			{name: "negation", patterns: "*\n!foo\n"},
			{name: "invalid", patterns: "!\n", wantError: "illegal exclusion pattern"},
			{name: "invalid-pattern", patterns: "[\n", wantError: "syntax error in pattern"},
		} {
			t.Run(instruction+"/"+tc.name, func(t *testing.T) {
				c := &contextClient{solve: func(ctx context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
					src := localContextSource(t, req.Definition.Def)
					require.JSONEq(t, `[".dockerignore"]`, src.Attrs[pb.AttrFollowPaths])
					files := map[string][]byte{}
					if tc.name != "missing" {
						files[".dockerignore"] = []byte(tc.patterns)
					}
					return &gwclient.Result{Ref: &contextReference{files: files}}, nil
				}}
				bc, err := dockerui.NewClient(c)
				require.NoError(t, err)
				var warnings []string
				res, err := Dockerfile2LLB(t.Context(), []byte("FROM scratch\n"+instruction+" foo /foo\n"+instruction+" bar /bar\n"), ConvertOpt{
					Client: bc,
					Warn: func(name, _, _, _ string, _ []parser.Range) {
						warnings = append(warnings, name)
					},
				})
				if tc.wantError != "" {
					require.ErrorContains(t, err, tc.wantError)
					return
				}
				require.NoError(t, err)
				def, err := res.State.Marshal(t.Context())
				require.NoError(t, err)
				src := localContextSource(t, def.Def)
				require.JSONEq(t, `["bar","foo"]`, src.Attrs[pb.AttrFollowPaths])
				var excludes []string
				if tc.patterns != "" {
					require.NoError(t, json.Unmarshal([]byte(src.Attrs[pb.AttrExcludePatterns]), &excludes))
				}
				switch tc.name {
				case "ignored":
					require.Equal(t, []string{"foo"}, excludes)
				case "negation":
					require.Equal(t, []string{"*", "!foo"}, excludes)
				default:
					require.Empty(t, src.Attrs[pb.AttrExcludePatterns])
				}
				if tc.wantWarning {
					require.Contains(t, warnings, linter.RuleCopyIgnoredFile.Name)
				} else {
					require.NotContains(t, warnings, linter.RuleCopyIgnoredFile.Name)
				}
				require.Equal(t, 1, c.solves, "ignore file must only be loaded once")
			})
		}
	}
}

func TestLocalContextAccessError(t *testing.T) {
	t.Parallel()
	for _, instruction := range []string{"COPY foo /foo", "ADD foo /foo", "RUN --mount=target=/src echo hello"} {
		t.Run(instruction, func(t *testing.T) {
			c := &contextClient{}
			bc, err := dockerui.NewClient(c)
			require.NoError(t, err)
			res, err := Dockerfile2LLB(t.Context(), []byte("FROM scratch\n"+instruction+"\n"), ConvertOpt{Client: bc})
			if err == nil {
				_, err = res.State.Marshal(t.Context())
			}
			require.ErrorContains(t, err, "local context accessed")
			require.Equal(t, 1, c.solves)
		})
	}
}

func TestLocalContextDeferredRequests(t *testing.T) {
	t.Parallel()
	for _, scan := range []bool{false, true} {
		name := "mount"
		df := "FROM scratch\nRUN --mount=source=foo,target=/src echo hello\n"
		args := map[string]string{}
		if scan {
			name = "sbom"
			df = "ARG BUILDKIT_SBOM_SCAN_CONTEXT\nFROM scratch\n"
			args[sbomScanContext] = "true"
		}
		t.Run(name, func(t *testing.T) {
			c := &contextClient{solve: func(context.Context, gwclient.SolveRequest) (*gwclient.Result, error) {
				return &gwclient.Result{Ref: &contextReference{files: map[string][]byte{".dockerignore": []byte("ignored\n")}}}, nil
			}}
			bc, err := dockerui.NewClient(c)
			require.NoError(t, err)
			res, err := Dockerfile2LLB(t.Context(), []byte(df), ConvertOpt{Client: bc, Config: dockerui.Config{BuildArgs: args}})
			require.NoError(t, err)
			require.Zero(t, c.solves, "conversion must defer context access")
			st := res.State
			if scan {
				_, err := st.Marshal(t.Context())
				require.NoError(t, err)
				require.Zero(t, c.solves, "image does not use the scan context")
				var ok bool
				st, ok = res.SBOM.Extras["context"]
				require.True(t, ok)
			}
			def, err := st.Marshal(t.Context())
			require.NoError(t, err)
			src := localContextSource(t, def.Def)
			require.JSONEq(t, `["ignored"]`, src.Attrs[pb.AttrExcludePatterns])
			if !scan {
				require.JSONEq(t, `["foo"]`, src.Attrs[pb.AttrFollowPaths])
			}
			require.Equal(t, 1, c.solves)
		})
	}
}

func TestLocalContextDockerignoreOverride(t *testing.T) {
	t.Parallel()
	df := []byte("FROM scratch\nCOPY foo /foo\n")
	c := &contextClient{solve: func(_ context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
		for _, dt := range req.Definition.Def {
			var op pb.Op
			require.NoError(t, op.Unmarshal(dt))
			if src := op.GetSource(); src != nil {
				require.Equal(t, "local://dockerfile", src.Identifier, "must not load the context's .dockerignore")
			}
		}
		return &gwclient.Result{Ref: &contextReference{files: map[string][]byte{
			"Dockerfile":              df,
			"Dockerfile.dockerignore": []byte("foo\n"),
		}}}, nil
	}}
	bc, err := dockerui.NewClient(c)
	require.NoError(t, err)
	src, err := bc.ReadEntrypoint(t.Context(), "Dockerfile")
	require.NoError(t, err)
	var warnings []string
	res, err := Dockerfile2LLB(t.Context(), src.Data, ConvertOpt{
		Client: bc,
		Warn: func(name, _, _, _ string, _ []parser.Range) {
			warnings = append(warnings, name)
		},
	})
	require.NoError(t, err)
	def, err := res.State.Marshal(t.Context())
	require.NoError(t, err)
	require.JSONEq(t, `["foo"]`, localContextSource(t, def.Def).Attrs[pb.AttrExcludePatterns])
	require.Contains(t, warnings, linter.RuleCopyIgnoredFile.Name)
	require.Equal(t, 1, c.solves, "only the Dockerfile source should have been solved")
}

func TestLocalContextLint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, dockerfile string
		wantSolves       int
		wantIgnoredWarn  bool
	}{
		{name: "unused", dockerfile: "FROM scratch\n"},
		{name: "used", dockerfile: "FROM scratch\nCOPY foo /foo\n", wantSolves: 1, wantIgnoredWarn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &contextClient{solve: func(context.Context, gwclient.SolveRequest) (*gwclient.Result, error) {
				return &gwclient.Result{Ref: &contextReference{files: map[string][]byte{
					".dockerignore": []byte("foo\n"),
				}}}, nil
			}}
			bc, err := dockerui.NewClient(c)
			require.NoError(t, err)
			dt := []byte(tc.dockerfile)
			src := llb.Local("dockerfile")
			def, err := src.Marshal(t.Context())
			require.NoError(t, err)
			smap := llb.NewSourceMap(&src, "Dockerfile", "Dockerfile", dt)
			smap.Definition = def
			res, err := DockerfileLint(t.Context(), dt, ConvertOpt{Client: bc, SourceMap: smap})
			require.NoError(t, err)
			require.Nil(t, res.Error)
			require.Equal(t, tc.wantSolves, c.solves)
			var ignored bool
			for _, warning := range res.Warnings {
				if warning.RuleName == linter.RuleCopyIgnoredFile.Name {
					ignored = true
				}
			}
			require.Equal(t, tc.wantIgnoredWarn, ignored)
		})
	}
}

// localContextSource finds the actual context source in a marshaled LLB graph.
func localContextSource(t *testing.T, def [][]byte) *pb.SourceOp {
	t.Helper()
	for _, dt := range def {
		var op pb.Op
		require.NoError(t, op.Unmarshal(dt))
		if src := op.GetSource(); src != nil && src.Identifier == "local://context" {
			return src
		}
	}
	t.Fatal("missing local context source")
	return nil
}

// contextClient rejects unconfigured solves, so an unused context cannot be
// accessed even if its source is later pruned from the final LLB graph.
type contextClient struct {
	gwclient.Client
	solves int
	solve  func(context.Context, gwclient.SolveRequest) (*gwclient.Result, error)
	opts   map[string]string
}

func (c *contextClient) BuildOpts() gwclient.BuildOpts {
	return gwclient.BuildOpts{
		Caps:    gwpb.Caps.CapSet(gwpb.Caps.All()),
		LLBCaps: pb.Caps.CapSet(pb.Caps.All()),
		Opts:    c.opts,
	}
}

func (c *contextClient) Inputs(context.Context) (map[string]llb.State, error) {
	return nil, nil
}

func (c *contextClient) Solve(ctx context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	c.solves++
	if c.solve != nil {
		return c.solve(ctx, req)
	}
	return nil, errors.New("local context accessed")
}

// contextReference supplies ignore files without a daemon or filesystem mount.
type contextReference struct {
	gwclient.Reference
	files map[string][]byte
}

func (r *contextReference) ReadFile(_ context.Context, req gwclient.ReadRequest) ([]byte, error) {
	if dt, ok := r.files[req.Filename]; ok {
		return dt, nil
	}
	return nil, os.ErrNotExist
}
