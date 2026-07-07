package client

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/content/proxy"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	fsutiltypes "github.com/tonistiigi/fsutil/types"
)

func checkAllReleasable(t *testing.T, c *Client, sb integration.Sandbox, checkContent bool) {
	cl, err := c.ControlClient().ListenBuildHistory(sb.Context(), &controlapi.BuildHistoryRequest{
		EarlyExit: true,
	})
	require.NoError(t, err)

	for {
		resp, err := cl.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		_, err = c.ControlClient().UpdateBuildHistory(sb.Context(), &controlapi.UpdateBuildHistoryRequest{
			Ref:    resp.Record.Ref,
			Delete: true,
		})
		require.NoError(t, err)
	}

	retries := 0
loop0:
	for {
		require.Greater(t, 20, retries)
		retries++
		du, err := c.DiskUsage(sb.Context())
		require.NoError(t, err)
		for _, d := range du {
			if d.InUse {
				time.Sleep(500 * time.Millisecond)
				continue loop0
			}
		}
		break
	}

	err = c.Prune(sb.Context(), nil, PruneAll)
	require.NoError(t, err)

	du, err := c.DiskUsage(sb.Context())
	require.NoError(t, err)
	require.Equal(t, 0, len(du))

	// examine contents of exported tars (requires containerd)
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		if checkContent {
			if err := workers.HasFeatureCompat(t, sb, workers.FeatureContentCheck); err == nil {
				store := proxy.NewContentStore(c.ContentClient())
				count := 0
				err := store.Walk(sb.Context(), func(info content.Info) error {
					count++
					return nil
				})
				require.NoError(t, err)
				require.Equal(t, 0, count)
			}
		}
		t.Logf("checkAllReleasable: skipping check for exported tars in non-containerd test")
		return
	}

	// TODO: make public pull helper function so this can be checked for standalone as well

	client, err := newContainerd(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	for _, ns := range []string{"buildkit", "buildkit_history"} {
		ctx := namespaces.WithNamespace(sb.Context(), ns)
		snapshotterName := sb.Snapshotter()
		snapshotService := client.SnapshotService(snapshotterName)

		checkLeases := func() {
			leases, err := client.LeasesService().List(ctx)
			require.NoError(t, err)
			count := 0
			for _, l := range leases {
				_, isTemp := l.Labels["buildkit/lease.temporary"]
				_, isExpire := l.Labels["containerd.io/gc.expire"]
				if isTemp && isExpire {
					continue
				}
				resources, err := client.LeasesService().ListResources(ctx, l)
				require.NoError(t, err)
				if len(resources) == 0 {
					continue
				}
				count++
				t.Logf("lease: %v", l)
				for _, r := range resources {
					t.Logf("lease resource: %v", r)
				}
			}
			require.Equal(t, 0, count)
		}

		if checkContent {
			images, err := client.ImageService().List(ctx)
			require.NoError(t, err)
			for _, img := range images {
				err := client.ImageService().Delete(ctx, img.Name)
				require.NoError(t, err)
			}
		}

		retries = 0
		for {
			count := 0
			err = snapshotService.Walk(ctx, func(context.Context, snapshots.Info) error {
				count++
				return nil
			})
			require.NoError(t, err)
			if count == 0 {
				break
			}
			require.Less(t, retries, 20)
			retries++
			time.Sleep(500 * time.Millisecond)
		}

		if !checkContent {
			checkLeases()
			return
		}

		retries = 0
		for {
			count := 0
			var infos []content.Info
			err = client.ContentStore().Walk(ctx, func(info content.Info) error {
				count++
				infos = append(infos, info)
				return nil
			})
			require.NoError(t, err)
			if count == 0 {
				break
			}
			if retries >= 50 {
				for _, info := range infos {
					t.Logf("content: %v %v %+v", info.Digest, info.Size, info.Labels)
					ra, err := client.ContentStore().ReaderAt(ctx, ocispecs.Descriptor{
						Digest: info.Digest,
						Size:   info.Size,
					})
					if err == nil {
						dt := make([]byte, 1024)
						n, err := ra.ReadAt(dt, 0)
						t.Logf("data: %+v %q", err, string(dt[:n]))
					}
				}
				require.FailNowf(t, "content still exists", "%+v", infos)
			}
			retries++
			time.Sleep(500 * time.Millisecond)
		}

		checkLeases()
	}
}

func ensureFile(t *testing.T, path string) {
	st, err := os.Stat(path)
	require.NoError(t, err, "expected file at %s", path)
	require.True(t, st.Mode().IsRegular())
}

func ensureFileContents(t *testing.T, path, expectedContents string) {
	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	actualContents := string(contents)
	require.Equal(t, expectedContents, actualContents,
		"file contents mismatch for %s\nexpected: %q\nactual: %q", path, expectedContents, actualContents)
}

// ensurePruneAll tries to ensure Prune completes with retries.
// Current cache implementation defers release-related logic using goroutine so
// there can be situation where a build has finished but the following prune doesn't
// cleanup cache because some records still haven't been released.
// This function tries to ensure prune by retrying it.
func ensurePruneAll(t *testing.T, c *Client, sb integration.Sandbox) {
	for i := range 2 {
		require.NoError(t, c.Prune(sb.Context(), nil, PruneAll))
		for range 20 {
			du, err := c.DiskUsage(sb.Context())
			require.NoError(t, err)
			if len(du) == 0 {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Logf("retrying prune(%d)", i)
	}
	t.Fatalf("failed to ensure prune")
}

func fixedWriteCloser(wc io.WriteCloser) filesync.FileOutputFunc {
	return func(map[string]string) (io.WriteCloser, error) {
		return wc, nil
	}
}

func newContainerd(cdAddress string) (*ctd.Client, error) {
	return ctd.New(cdAddress, ctd.WithTimeout(60*time.Second))
}

func parseFSMetadata(t *testing.T, dt []byte) []fsutiltypes.Stat {
	var m []fsutiltypes.Stat
	for len(dt) > 0 {
		var s fsutiltypes.Stat
		n := binary.LittleEndian.Uint32(dt[:4])
		dt = dt[4:]
		err := s.Unmarshal(dt[:n])
		require.NoError(t, err)
		m = append(m, *s.CloneVT())
		dt = dt[n:]
	}
	return m
}

func readFileInImage(ctx context.Context, t *testing.T, c *Client, ref, path string) ([]byte, error) {
	def, err := llb.Image(ref).Marshal(ctx)
	if err != nil {
		return nil, err
	}
	destDir := t.TempDir()

	_, err = c.Solve(ctx, def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: destDir,
			},
		},
	}, nil)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(destDir, filepath.Clean(path)))
}

type imageTimestamps struct {
	FromImage      []string // from img.Created and img.[]History.Created
	FromAnnotation string   // from index.Manifests[0].Annotations["org.opencontainers.image.created"]
}

func readImageTimestamps(dt []byte) (*imageTimestamps, error) {
	m, err := testutil.ReadTarToMap(dt, false)
	if err != nil {
		return nil, err
	}

	if _, ok := m["oci-layout"]; !ok {
		return nil, errors.Errorf("no oci-layout")
	}

	var index ocispecs.Index
	if err := json.Unmarshal(m[ocispecs.ImageIndexFile].Data, &index); err != nil {
		return nil, err
	}
	if len(index.Manifests) != 1 {
		return nil, errors.Errorf("invalid manifest count %d", len(index.Manifests))
	}

	var res imageTimestamps
	res.FromAnnotation = index.Manifests[0].Annotations[ocispecs.AnnotationCreated]

	var mfst ocispecs.Manifest
	if err := json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+index.Manifests[0].Digest.Hex()].Data, &mfst); err != nil {
		return nil, err
	}
	// don't unmarshal to image type so we get the original string value
	type history struct {
		Created string `json:"created"`
	}

	img := struct {
		History []history `json:"history"`
		Created string    `json:"created"`
	}{}

	if err := json.Unmarshal(m[ocispecs.ImageBlobsDir+"/sha256/"+mfst.Config.Digest.Hex()].Data, &img); err != nil {
		return nil, err
	}

	res.FromImage = []string{
		img.Created,
	}
	for _, h := range img.History {
		res.FromImage = append(res.FromImage, h.Created)
	}
	return &res, nil
}

func requiresLinux(t *testing.T) {
	integration.SkipOnPlatform(t, "!linux")
}
