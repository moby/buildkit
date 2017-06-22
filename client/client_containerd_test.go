// +build containerd

package client

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func runContainerd() (string, func(), error) {
	tmpdir, err := ioutil.TempDir("", "containerd")
	if err != nil {
		return "", nil, err
	}

	address := filepath.Join(tmpdir, "containerd.sock")

	args := append([]string{}, "containerd", "--root", tmpdir, "--root", filepath.Join(tmpdir, "state"), "--address", address)

	cmd := exec.Command(args[0], args[1:]...)
	// cmd.Stderr = os.Stdout
	// cmd.Stdout = os.Stdout

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpdir)
		return "", nil, err
	}

	time.Sleep(200 * time.Millisecond) // TODO

	return address, func() {
		os.RemoveAll(tmpdir)

		// tear down the daemon and resources created
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		if _, err := cmd.Process.Wait(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.RemoveAll(tmpdir)
	}, nil
}

func setupContainerd() (func(), error) {
	containerdSock, cleanupContainerd, err := runContainerd()
	if err != nil {
		return nil, err
	}
	sock, cleanup, err := runBuildd([]string{"buildd-containerd", "--containerd", containerdSock})
	if err != nil {
		cleanupContainerd()
		return nil, err
	}

	clientAddressContainerd = sock
	time.Sleep(100 * time.Millisecond) // TODO
	return func() {
		cleanup()
		cleanupContainerd()
	}, nil
}

func TestCallDiskUsageContainerd(t *testing.T) {
	testCallDiskUsage(t, clientAddressContainerd)
}

func TestBuildMultiMountContainerd(t *testing.T) {
	testBuildMultiMount(t, clientAddressContainerd)
}
