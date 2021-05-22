package solver

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func init() {
	integration.InitOCIWorker()
	integration.InitContainerdWorker()
}

func TestJobsIntegration(t *testing.T) {
	mirrors := integration.WithMirroredImages(integration.OfficialImages("busybox:latest"))
	integration.Run(t, []integration.Test{
		testParallelism,
	},
		mirrors,
		integration.WithMatrix("max-parallelism", map[string]interface{}{
			"single":    maxParallelismSingle,
			"unlimited": maxParallelismUnlimited,
		}),
	)
}

func testParallelism(t *testing.T, sb integration.Sandbox) {
	ctx := context.TODO()

	c, err := client.New(ctx, sb.Address())
	require.NoError(t, err)
	defer c.Close()

	cacheMount := llb.AddMount(
		"/shared", llb.Scratch(),
		llb.AsPersistentCacheDir("shared", llb.CacheMountShared))
	run1 := llb.Image("busybox:latest").Run(
		llb.Args([]string{
			"/bin/sh", "-c",
			"touch /shared/signal1 && i=0; while [ ! -f /shared/signal2 ] && [ $i -lt 10 ]; do i=$((i+1)); sleep 1; done",
		}),
		cacheMount,
	).Root()
	d1, err := run1.Marshal(ctx)
	require.NoError(t, err)
	run2 := llb.Image("busybox:latest").Run(
		llb.Args([]string{
			"/bin/sh", "-c",
			"touch /shared/signal2 && i=0; while [ ! -f /shared/signal1 ] && [ $i -lt 10 ]; do i=$((i+1)); sleep 1; done",
		}),
		cacheMount,
	).Root()
	d2, err := run2.Marshal(ctx)
	require.NoError(t, err)

	timeStart := time.Now()
	eg, egCtx := errgroup.WithContext(ctx)
	tmpDir, err := ioutil.TempDir("", "solver-jobs-test-")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)
	solveOpt := client.SolveOpt{
		LocalDirs: map[string]string{"cache": tmpDir},
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
	if maxParallelism == maxParallelismSingle {
		require.Greater(t, elapsed, 10*time.Second, "parallelism not restricted")
	} else if maxParallelism == maxParallelismUnlimited {
		require.Less(t, elapsed, 10*time.Second, "parallelism hindered")
	}
}

type parallelismSetterSingle struct{}

func (*parallelismSetterSingle) UpdateConfigFile(in string) string {
	return in + "\n\n[worker.oci]\n  max-parallelism = 1\n\n[worker.containerd]\n  max-parallelism = 1\n"
}

var maxParallelismSingle integration.ConfigUpdater = &parallelismSetterSingle{}

type parallelismSetterUnlimited struct{}

func (*parallelismSetterUnlimited) UpdateConfigFile(in string) string {
	return in
}

var maxParallelismUnlimited integration.ConfigUpdater = &parallelismSetterUnlimited{}
