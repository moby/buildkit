package dockerfile

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/content/proxy"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/containerd/platforms"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/moby/buildkit/util/stack"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func testExportedHistory(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	baseImage := integration.UnixOrWindows("busybox", "nanoserver:latest")
	workdir := integration.UnixOrWindows("/", "C:\\workdir")
	// Use JSON array form to avoid shell redirect issues on Windows
	lsCmd := integration.UnixOrWindows(`["ls"]`, `["cmd", "/S", "/C", "dir"]`)

	// using multi-stage to test that history is scoped to one stage
	dockerfile := fmt.Appendf(nil, `
FROM %s AS base
ENV foo=bar
COPY foo /foo2
FROM %s
LABEL lbl=val
COPY --from=base foo2 foo3
WORKDIR %s
RUN echo bar > foo4
RUN %s
EXPOSE 2375 5000 1234/udp
`, baseImage, baseImage, workdir, lsCmd)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("contents0"), 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)

	target := "example.com/moby/dockerfilescratch:test"
	cmd := sb.Cmd(args + " --output type=image,name=" + target)
	require.NoError(t, cmd.Run())

	// TODO: expose this test to OCI worker
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("rest of test requires containerd worker")
	}

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(sb.Context(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, client.ContentStore(), desc)
	require.NoError(t, err)

	var ociimg ocispecs.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.Equal(t, "layers", ociimg.RootFS.Type)
	// this depends on base image. should be ok after freezing images
	expectedDiffIDs := integration.UnixOrWindows(4, 5)
	require.Equal(t, expectedDiffIDs, len(ociimg.RootFS.DiffIDs))

	// Windows nanoserver has 1 base history entry (for 2 layers) vs busybox's 2 entries (for 1 layer)
	// Total: busybox (2 base + 6 new = 8), nanoserver (1 base + 6 new = 7)
	expectedHistoryCount := integration.UnixOrWindows(8, 7)
	require.Equal(t, expectedHistoryCount, len(ociimg.History))

	// History indices: Windows has 1 base entry (starts at index 1), Unix has 2 base entries (starts at index 2)
	historyOffset := integration.UnixOrWindows(2, 1)

	require.Contains(t, ociimg.History[historyOffset].CreatedBy, "lbl=val")
	require.Equal(t, true, ociimg.History[historyOffset].EmptyLayer)
	require.NotNil(t, ociimg.History[historyOffset].Created)
	require.Contains(t, ociimg.History[historyOffset+1].CreatedBy, "COPY foo2 foo3")
	require.Equal(t, false, ociimg.History[historyOffset+1].EmptyLayer)
	require.NotNil(t, ociimg.History[historyOffset+1].Created)
	// The history records the Dockerfile command as written, so it's "/workdir" on both platforms
	// even though Windows converts it to "C:\workdir" at runtime
	expectedWorkdir := integration.UnixOrWindows("WORKDIR /", "WORKDIR /workdir")
	require.Contains(t, ociimg.History[historyOffset+2].CreatedBy, expectedWorkdir)
	// On Windows, WORKDIR may create a directory and thus not be an empty layer
	expectedWorkdirEmptyLayer := integration.UnixOrWindows(true, false)
	require.Equal(t, expectedWorkdirEmptyLayer, ociimg.History[historyOffset+2].EmptyLayer)
	require.NotNil(t, ociimg.History[historyOffset+2].Created)
	require.Contains(t, ociimg.History[historyOffset+3].CreatedBy, "echo bar > foo4")
	require.Equal(t, false, ociimg.History[historyOffset+3].EmptyLayer)
	require.NotNil(t, ociimg.History[historyOffset+3].Created)
	expectedLs := integration.UnixOrWindows("RUN ls", "RUN cmd /S /C dir")
	require.Contains(t, ociimg.History[historyOffset+4].CreatedBy, expectedLs)
	require.Equal(t, false, ociimg.History[historyOffset+4].EmptyLayer)
	require.NotNil(t, ociimg.History[historyOffset+4].Created)
	// On Windows, EXPOSE may be formatted differently in history
	expectedExpose := integration.UnixOrWindows("EXPOSE [1234/udp 2375/tcp 5000/tcp]", "EXPOSE")
	require.Contains(t, ociimg.History[historyOffset+5].CreatedBy, expectedExpose)
	require.Equal(t, true, ociimg.History[historyOffset+5].EmptyLayer)
	require.NotNil(t, ociimg.History[historyOffset+5].Created)
}

// moby/buildkit#5505
func testExportedHistoryFlattenArgs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	dockerfile := []byte(integration.UnixOrWindows(`
FROM busybox
ARG foo=bar
ARG bar=123
ARG foo=bar2
RUN ls /etc/
`, `
FROM nanoserver:latest
ARG foo=bar
ARG bar=123
ARG foo=bar2
RUN dir C:\Windows
`))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	workers.CheckFeatureCompat(t, sb, workers.FeatureImageExporter)
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)

	registry, err := sb.NewRegistry()
	if errors.Is(err, integration.ErrRequirements) {
		t.Skip(err.Error())
	}
	require.NoError(t, err)

	target := registry + "/buildkit/testargduplicate:latest"
	cmd := sb.Cmd(args + " --output type=image,push=true,name=" + target)
	require.NoError(t, cmd.Run())

	desc, provider, err := contentutil.ProviderFromRef(target)
	require.NoError(t, err)

	imgs, err := testutil.ReadImages(sb.Context(), provider, desc)
	require.NoError(t, err)

	require.Equal(t, 1, len(imgs.Images))

	history := imgs.Images[0].Img.History

	firstNonBase := -1
	for i, h := range history {
		if h.CreatedBy == "ARG foo=bar" {
			firstNonBase = i
			break
		}
	}
	require.Greater(t, firstNonBase, 0)

	require.Len(t, history, firstNonBase+4)
	require.Contains(t, history[firstNonBase+1].CreatedBy, "ARG bar=123")
	require.Contains(t, history[firstNonBase+2].CreatedBy, "ARG foo=bar2")

	runLine := history[firstNonBase+3].CreatedBy
	expectedCmd := integration.UnixOrWindows("ls /etc/", "dir C:\\Windows")
	require.Contains(t, runLine, expectedCmd)
	require.NotContains(t, runLine, "ARG foo=bar")
	require.Contains(t, runLine, "RUN |2 foo=bar2 bar=123 ")
}

func testHistoryError(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY notexist /foo
`,
		`
FROM nanoserver
COPY notexist /foo
`,
	))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	ref := identity.NewID()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Ref: ref,
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.Error(t, err)

	expectedError := err

	cl, err := c.ControlClient().ListenBuildHistory(sb.Context(), &controlapi.BuildHistoryRequest{
		EarlyExit: true,
		Ref:       ref,
	})
	require.NoError(t, err)

	got := false
	for {
		resp, err := cl.Recv()
		if errors.Is(err, io.EOF) {
			require.Equal(t, true, got, "expected error was %+v", expectedError)
			break
		}
		require.NoError(t, err)
		require.NotEmpty(t, resp.Record.Error)
		got = true

		require.Len(t, resp.Record.Error.Details, 0)
		require.Contains(t, resp.Record.Error.Message, "/notexist")

		extErr := resp.Record.ExternalError
		require.NotNil(t, extErr)

		require.Greater(t, extErr.Size, int64(0))
		require.Equal(t, "application/vnd.googeapis.google.rpc.status+proto", extErr.MediaType)

		bkstore := proxy.NewContentStore(c.ContentClient())

		dt, err := content.ReadBlob(ctx, bkstore, ocispecs.Descriptor{
			MediaType: extErr.MediaType,
			Digest:    digest.Digest(extErr.Digest),
			Size:      extErr.Size,
		})
		require.NoError(t, err)

		var st statuspb.Status
		err = proto.Unmarshal(dt, &st)
		require.NoError(t, err)

		require.Equal(t, resp.Record.Error.Code, st.Code)
		require.Equal(t, resp.Record.Error.Message, st.Message)

		details := make([]*anypb.Any, len(st.Details))
		for i, d := range st.Details {
			details[i] = &anypb.Any{
				TypeUrl: d.TypeUrl,
				Value:   d.Value,
			}
		}

		err = grpcerrors.FromGRPC(status.FromProto(&statuspb.Status{
			Code:    st.Code,
			Message: st.Message,
			Details: details,
		}).Err())

		require.Error(t, err)

		// typed error has stacks
		stacks := stack.Traces(err)
		require.Greater(t, len(stacks), 1)

		// contains vertex metadata
		var ve *errdefs.VertexError
		if errors.As(err, &ve) {
			_, err := digest.Parse(ve.Digest)
			require.NoError(t, err)
		} else {
			t.Fatalf("did not find vertex error")
		}

		// source points to Dockerfile
		sources := errdefs.Sources(err)
		require.Len(t, sources, 1)

		src := sources[0]
		require.Equal(t, "Dockerfile", src.Info.Filename)
		require.Equal(t, dockerfile, src.Info.Data)
		require.NotNil(t, src.Info.Definition)
	}
}

func testHistoryFinalizeTrace(t *testing.T, sb integration.Sandbox) {
	workers.CheckFeatureCompat(t, sb, workers.FeatureDirectPush)
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	f := getFrontend(t, sb)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
COPY Dockerfile /foo
`,
		`
FROM nanoserver
COPY Dockerfile /foo
`,
	))
	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	ref := identity.NewID()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Ref: ref,
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	_, err = c.ControlClient().UpdateBuildHistory(sb.Context(), &controlapi.UpdateBuildHistoryRequest{
		Ref:      ref,
		Finalize: true,
	})
	require.NoError(t, err)

	cl, err := c.ControlClient().ListenBuildHistory(sb.Context(), &controlapi.BuildHistoryRequest{
		EarlyExit: true,
		Ref:       ref,
	})
	require.NoError(t, err)

	got := false
	for {
		resp, err := cl.Recv()
		if errors.Is(err, io.EOF) {
			require.Equal(t, true, got)
			break
		}
		require.NoError(t, err)
		got = true

		trace := resp.Record.Trace
		require.NotEmpty(t, trace)

		require.NotEmpty(t, trace.Digest)
	}
}
