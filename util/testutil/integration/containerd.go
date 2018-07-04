package integration

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func init() {
	// empty value stands for system-wide PATH, which would contain containerd v1.1.
	paths := []string{"", "/opt/containerd-1.0/bin"}
	for _, path := range paths {
		register(&containerd{
			containerd:     filepath.Join(path, "containerd"),
			containerdShim: filepath.Join(path, "containerd-shim"),
		})
	}
}

type containerd struct {
	containerd     string
	containerdShim string
}

func (c *containerd) Name() string {
	return c.containerd
}

func (c *containerd) New() (sb Sandbox, cl func() error, err error) {
	if err := lookupBinary(c.containerd); err != nil {
		return nil, nil, err
	}
	if err := lookupBinary(c.containerdShim); err != nil {
		return nil, nil, err
	}
	if err := lookupBinary("buildkitd"); err != nil {
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
	config := fmt.Sprintf(`root = %q
state = %q

[grpc]
  address = %q

[debug]
  level = "debug"

[plugins]
  [plugins.linux]
    shim = %q
`, filepath.Join(tmpdir, "root"), filepath.Join(tmpdir, "state"), address, c.containerdShim)
	configFile := filepath.Join(tmpdir, "config.toml")
	if err := ioutil.WriteFile(configFile, []byte(config), 0644); err != nil {
		return nil, nil, err
	}

	cmd := exec.Command(c.containerd, "--config", configFile)

	logs := map[string]*bytes.Buffer{}

	if stop, err := startCmd(cmd, logs); err != nil {
		return nil, nil, err
	} else {
		deferF.append(stop)
	}
	if err := waitUnix(address, 5*time.Second); err != nil {
		return nil, nil, err
	}

	buildkitdSock, stop, err := runBuildkitd([]string{"buildkitd",
		"--oci-worker=false",
		"--containerd-worker=true",
		"--containerd-worker-addr", address}, logs, 0, 0)
	if err != nil {
		return nil, nil, err
	}
	deferF.append(stop)

	return &cdsandbox{address: address, sandbox: sandbox{address: buildkitdSock, logs: logs, cleanup: deferF, rootless: false}}, cl, nil
}

type cdsandbox struct {
	sandbox
	address string
}

func (s *cdsandbox) ContainerdAddress() string {
	return s.address
}
