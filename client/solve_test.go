package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/client/llb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestLocalCacheStoreLockAndResume(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	first, err := newLocalCacheStore(dir)
	require.NoError(t, err)
	second, err := newLocalCacheStore(dir)
	require.NoError(t, err)
	data := []byte("resumable cache layer")
	desc := ocispecs.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data))}
	opts := []content.WriterOpt{content.WithRef("layer"), content.WithDescriptor(desc)}
	w, err := first.Writer(ctx, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, w.Close()) })
	_, err = w.Write(data[:5])
	require.NoError(t, err)

	_, err = second.Writer(ctx, opts...)
	require.ErrorIs(t, err, cerrdefs.ErrUnavailable)
	require.ErrorIs(t, second.Abort(ctx, "layer"), cerrdefs.ErrUnavailable)
	// Cancellation of OpenWriter's retry must not disturb the active writer.
	cancelled, cancel := context.WithCancelCause(ctx)
	cancel(errors.New("cancel retry"))
	_, err = content.OpenWriter(cancelled, second, opts...)
	require.ErrorIs(t, err, cerrdefs.ErrUnavailable)

	// Independent references can still be written concurrently.
	other, err := second.Writer(ctx, content.WithRef("other"))
	require.NoError(t, err)
	require.NoError(t, other.Close())
	require.NoError(t, second.Abort(ctx, "other"))
	require.NoError(t, w.Close())

	resumed, err := second.Writer(ctx, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resumed.Close()) })
	status, err := resumed.Status()
	require.NoError(t, err)
	require.EqualValues(t, 5, status.Offset)
	_, err = resumed.Write(data[5:])
	require.NoError(t, err)
	require.NoError(t, resumed.Commit(ctx, desc.Size, desc.Digest))
	blob, err := content.ReadBlob(ctx, first, desc)
	require.NoError(t, err)
	require.Equal(t, data, blob)
	entries, err := os.ReadDir(filepath.Join(dir, "ingest"))
	require.NoError(t, err)
	require.Empty(t, entries, "resuming an interrupted export must reclaim its ingest")

	// An already-existing blob must not leave its reference locked.
	_, err = first.Writer(ctx, opts...)
	require.ErrorIs(t, err, cerrdefs.ErrAlreadyExists)
	require.NoError(t, second.Abort(ctx, "layer"))
}

func TestLocalCacheStoreCommitError(t *testing.T) {
	for _, failure := range []string{"size", "option"} {
		t.Run(failure, func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			first, err := newLocalCacheStore(dir)
			require.NoError(t, err)
			second, err := newLocalCacheStore(dir)
			require.NoError(t, err)
			w, err := first.Writer(ctx, content.WithRef("layer"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, w.Close()) })
			if failure == "size" {
				err = w.Commit(ctx, 1, "")
				require.ErrorIs(t, err, cerrdefs.ErrFailedPrecondition)
			} else {
				sentinel := errors.New("invalid commit option")
				err = w.Commit(ctx, 0, "", func(*content.Info) error { return sentinel })
				require.ErrorIs(t, err, sentinel)
			}
			// Commit closes the writer even on failure, including the file lock.
			require.NoError(t, second.Abort(ctx, "layer"))
		})
	}
}

func TestSolveRejectsInvalidLocalExporterMode(t *testing.T) {
	st := llb.Scratch().File(
		llb.Mkfile("fresh.txt", 0600, []byte("fresh")),
	)
	def, err := st.Marshal(t.Context())
	require.NoError(t, err)

	_, err = (&Client{}).Solve(t.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: t.TempDir(),
				Attrs: map[string]string{
					"mode": "backup",
				},
			},
		},
	}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, `invalid local exporter mode "backup"`)
}
