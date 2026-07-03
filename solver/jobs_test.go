package solver

import (
	"fmt"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

func init() {
	workers.InitOCIWorker()
	workers.InitContainerdWorker()
}

func TestJobsIntegration(t *testing.T) {
	mirrors := integration.WithMirroredImages(integration.OfficialImages(
		integration.UnixOrWindows("busybox:latest", "nanoserver:latest"),
	))
	integration.Run(t, integration.TestFuncs(
		testParallelism,
	),
		mirrors,
		integration.WithMatrix("max-parallelism", map[string]any{
			"single":    maxParallelismSingle,
			"unlimited": maxParallelismUnlimited,
		}),
	)
}

// testParallelism verifies that two independent builds can run concurrently by
// using a shared cache mount for signaling. Each build creates a signal file and
// polls for the other's signal. With unlimited parallelism both finish under 10s;
// with single parallelism they run sequentially and exceed 10s.
func testParallelism(t *testing.T, sb integration.Sandbox) {
	ctx := sb.Context()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	imgName := integration.UnixOrWindows("busybox:latest", "nanoserver:latest")
	cacheMountSrc := integration.UnixOrWindows(llb.Scratch(), llb.Image(imgName))
	// sharedDir is the single source of truth for the cache mount target.
	// Using it for both llb.AddMount's destination and the command strings
	// avoids relying on any implicit "/shared" -> "C:\shared" mapping in WCOW.
	sharedDir := integration.UnixOrWindows("/shared", `C:\shared`)
	cacheMount := llb.AddMount(
		sharedDir, cacheMountSrc,
		llb.AsPersistentCacheDir("shared", llb.CacheMountShared))

	// On Linux, use sh to touch a signal file and poll for the peer's signal.
	// On Windows (nanoserver), use cmd with copy+ping to achieve the same
	// signaling: "copy nul" creates an empty file, and "ping -n 2 127.0.0.1"
	// provides a ~1-second delay per iteration.
	cmd1 := integration.UnixOrWindows(
		[]string{"/bin/sh", "-c",
			fmt.Sprintf("touch %[1]s/signal1 && i=0; while [ ! -f %[1]s/signal2 ] && [ $i -lt 10 ]; do i=$((i+1)); sleep 1; done", sharedDir)},
		[]string{"cmd", "/S", "/C",
			fmt.Sprintf(`copy nul %[1]s\signal1 >nul & for /L %%i in (1,1,10) do @if not exist %[1]s\signal2 ping -n 2 127.0.0.1 >nul`, sharedDir)},
	)
	run1 := llb.Image(imgName).Run(
		llb.Args(cmd1),
		cacheMount,
	).Root()
	d1, err := run1.Marshal(ctx)
	require.NoError(t, err)

	cmd2 := integration.UnixOrWindows(
		[]string{"/bin/sh", "-c",
			fmt.Sprintf("touch %[1]s/signal2 && i=0; while [ ! -f %[1]s/signal1 ] && [ $i -lt 10 ]; do i=$((i+1)); sleep 1; done", sharedDir)},
		[]string{"cmd", "/S", "/C",
			fmt.Sprintf(`copy nul %[1]s\signal2 >nul & for /L %%i in (1,1,10) do @if not exist %[1]s\signal1 ping -n 2 127.0.0.1 >nul`, sharedDir)},
	)
	run2 := llb.Image(imgName).Run(
		llb.Args(cmd2),
		cacheMount,
	).Root()
	d2, err := run2.Marshal(ctx)
	require.NoError(t, err)

	timeStart := time.Now()
	eg, egCtx := errgroup.WithContext(ctx)
	solveOpt := client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{"cache": integration.Tmpdir(t)},
	}
	eg.Go(func() error {
		_, err := c.Solve(egCtx, d1, solveOpt, nil)
		return err
	})
	eg.Go(func() error {
		_, err := c.Solve(egCtx, d2, solveOpt, nil)
		return err
	})
	err = eg.Wait()
	require.NoError(t, err)

	elapsed := time.Since(timeStart)

	maxParallelism := sb.Value("max-parallelism")
	switch maxParallelism {
	case maxParallelismSingle:
		require.Greater(t, elapsed, 10*time.Second, "parallelism not restricted")
	case maxParallelismUnlimited:
		require.Less(t, elapsed, 10*time.Second, "parallelism hindered")
	}
}

type parallelismSetterSingle struct{}

func (*parallelismSetterSingle) UpdateConfigFile(in string) (string, func() error) {
	return in + "\n\n[worker.oci]\n  max-parallelism = 1\n\n[worker.containerd]\n  max-parallelism = 1\n", nil
}

var maxParallelismSingle integration.ConfigUpdater = &parallelismSetterSingle{}

type parallelismSetterUnlimited struct{}

func (*parallelismSetterUnlimited) UpdateConfigFile(in string) (string, func() error) {
	return in, nil
}

var maxParallelismUnlimited integration.ConfigUpdater = &parallelismSetterUnlimited{}
