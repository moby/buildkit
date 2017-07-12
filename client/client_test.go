package client

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/stretchr/testify/assert"
)

var clientAddressStandalone string
var clientAddressContainerd string

func TestMain(m *testing.M) {
	if testing.Short() {
		os.Exit(m.Run())
	}

	cleanup, err := setupStandalone()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer cleanup()

	cleanup, err = setupContainerd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer cleanup()

	os.Exit(m.Run())
}

func runBuildd(args []string) (string, func(), error) {
	tmpdir, err := ioutil.TempDir("", "buildd")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmpdir)

	address := filepath.Join(tmpdir, "buildd.sock")
	if runtime.GOOS == "windows" {
		address = "//./pipe/buildd-" + filepath.Base(tmpdir)
	}

	args = append(args, "--root", tmpdir, "--socket", address, "--debug")

	cmd := exec.Command(args[0], args[1:]...)
	// cmd.Stderr = os.Stdout
	// cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}

	return address, func() {
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

func requiresLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}
}

func testCallDiskUsage(t *testing.T, address string) {
	c, err := New(address)
	assert.Nil(t, err)
	_, err = c.DiskUsage(context.TODO())
	assert.Nil(t, err)
}

func testBuildMultiMount(t *testing.T, address string) {
	requiresLinux(t)
	t.Parallel()
	c, err := New(address)
	assert.Nil(t, err)

	alpine := llb.Image("docker.io/library/alpine:latest")
	ls := alpine.Run(llb.Shlex("/bin/ls -l"))
	busybox := llb.Image("docker.io/library/busybox:latest")
	cp := ls.Run(llb.Shlex("/bin/cp -a /busybox/etc/passwd baz"))
	cp.AddMount("/busybox", busybox)

	dt, err := cp.Marshal()
	assert.Nil(t, err)

	buf := bytes.NewBuffer(nil)
	err = llb.WriteTo(dt, buf)
	assert.Nil(t, err)

	err = c.Solve(context.TODO(), buf, nil, "", nil)
	assert.Nil(t, err)
}
