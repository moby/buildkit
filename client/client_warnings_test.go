package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/testutil/integration"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func testWarnings(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)

	ctx := sb.Context()

	c, err := New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	product := "buildkit_test"

	b := func(ctx context.Context, c client.Client) (*client.Result, error) {
		st := llb.Scratch().File(llb.Mkfile("/dummy", 0o600, []byte("foo")))

		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal state")
		}

		dgst, _, _, _, err := st.Output().Vertex(ctx, def.Constraints).Marshal(ctx, def.Constraints)
		if err != nil {
			return nil, err
		}

		r, err := c.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to solve")
		}

		require.NoError(t, c.Warn(ctx, dgst, "this is warning", client.WarnOpts{
			Level: 3,
			SourceInfo: &pb.SourceInfo{
				Filename: "mydockerfile",
				Data:     []byte("filedata"),
			},
			Range: []*pb.Range{
				{Start: &pb.Position{Line: 2}, End: &pb.Position{Line: 4}},
			},
			Detail: [][]byte{[]byte("this is detail"), []byte("and more detail")},
			URL:    "https://example.com",
		}))

		return r, nil
	}

	wc := newWarningsCapture()

	_, err = c.Build(ctx, SolveOpt{}, product, b, wc.status)
	require.NoError(t, err)

	warnings := wc.wait()

	require.Equal(t, 1, len(wc.vertexes))
	require.Equal(t, 1, len(warnings))

	w := warnings[0]

	require.Equal(t, "this is warning", string(w.Short))
	require.Equal(t, 2, len(w.Detail))
	require.Equal(t, "this is detail", string(w.Detail[0]))
	require.Equal(t, "and more detail", string(w.Detail[1]))
	require.Equal(t, "https://example.com", w.URL)
	require.Equal(t, 3, w.Level)
	_, ok := wc.vertexes[w.Vertex]
	require.True(t, ok)

	require.Equal(t, "mydockerfile", w.SourceInfo.Filename)
	require.Equal(t, "filedata", string(w.SourceInfo.Data))
	require.Equal(t, 1, len(w.Range))
	require.Equal(t, int32(2), w.Range[0].Start.Line)
	require.Equal(t, int32(4), w.Range[0].End.Line)

	checkAllReleasable(t, c, sb, true)
}

type warningsCapture struct {
	status     chan *SolveStatus
	statusDone chan struct{}
	done       chan struct{}
	warnings   []*VertexWarning
	vertexes   map[digest.Digest]struct{}
}

func newWarningsCapture() *warningsCapture {
	w := &warningsCapture{
		status:     make(chan *SolveStatus),
		statusDone: make(chan struct{}),
		done:       make(chan struct{}),
		vertexes:   map[digest.Digest]struct{}{},
	}

	go func() {
		defer close(w.statusDone)
		for {
			select {
			case st, ok := <-w.status:
				if !ok {
					return
				}
				for _, s := range st.Vertexes {
					w.vertexes[s.Digest] = struct{}{}
				}
				w.warnings = append(w.warnings, st.Warnings...)
			case <-w.done:
				return
			}
		}
	}()

	return w
}

func (w *warningsCapture) wait() []*VertexWarning {
	select {
	case <-w.statusDone:
	case <-time.After(10 * time.Second):
		close(w.done)
	}

	<-w.statusDone
	return w.warnings
}

type warningsListOutput []*VertexWarning

func (w warningsListOutput) String() string {
	if len(w) == 0 {
		return ""
	}
	var b strings.Builder

	for _, warn := range w {
		_, _ = b.Write(warn.Short)
	}
	return b.String()
}
