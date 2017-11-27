package integration

import (
	"bytes"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func init() {
	register(&containerd{})
}

type containerd struct {
}

func (c *containerd) Name() string {
	return "containerd"
}

func (c *containerd) New() (sb Sandbox, cl func() error, err error) {
	if err := lookupBinary("containerd"); err != nil {
		return nil, nil, err
	}
	if err := lookupBinary("buildd-containerd"); err != nil {
		return nil, nil, err
	}
	if err := requireRoot(); err != nil {
		return nil, nil, err
	}

	deferF := &multiCloser{}
	cl = deferF.F()

	defer func() {
		if err != nil {
			deferF.F()()
			cl = nil
		}
	}()

	tmpdir, err := ioutil.TempDir("", "bktest_containerd")
	if err != nil {
		return nil, nil, err
	}

	deferF.append(func() error { return os.RemoveAll(tmpdir) })

	address := filepath.Join(tmpdir, "containerd.sock")
	args := append([]string{}, "containerd", "--root", filepath.Join(tmpdir, "root"), "--root", filepath.Join(tmpdir, "state"), "--address", address)

	cmd := exec.Command(args[0], args[1:]...)

	logs := map[string]*bytes.Buffer{}

	if stop, err := startCmd(cmd, logs); err != nil {
		return nil, nil, err
	} else {
		deferF.append(stop)
	}
	if err := waitUnix(address, 5*time.Second); err != nil {
		return nil, nil, err
	}

	builddSock, stop, err := runBuildd([]string{"buildd-containerd", "--containerd", address}, logs)
	if err != nil {
		return nil, nil, err
	}
	deferF.append(stop)

	return &sandbox{address: builddSock, logs: logs}, cl, nil
}
