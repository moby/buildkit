package dockerd

import (
	"bytes"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const fakeDockerdEnv = "BUILDKIT_TEST_FAKE_DOCKERD"

func TestMain(m *testing.M) {
	if os.Getenv(fakeDockerdEnv) == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStopWithErrorStopsDaemonOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("os.Interrupt is unsupported only on Windows")
	}
	self, err := os.Executable()
	require.NoError(t, err)

	d, err := NewDaemon(t.TempDir(), WithBinary(self), WithExtraEnv([]string{fakeDockerdEnv + "=1"}))
	require.NoError(t, err)
	require.NoError(t, d.StartWithError(map[string]*bytes.Buffer{}))

	require.NoError(t, d.StopWithError())
	select {
	case <-d.Wait:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon still running after StopWithError")
	}
}
