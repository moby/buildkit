//go:build !windows
// +build !windows

package integration

import (
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/pkg/errors"
)

func waitSocket(address string, d time.Duration, cmd *exec.Cmd) error {
	address = strings.TrimPrefix(address, "unix://")
	addr, err := net.ResolveUnixAddr("unix", address)
	if err != nil {
		return errors.Wrapf(err, "failed resolving unix addr: %s", address)
	}

	step := 50 * time.Millisecond
	i := 0
	for {
		if cmd != nil && cmd.ProcessState != nil {
			return errors.Errorf("process exited: %s", cmd.String())
		}

		if conn, err := net.DialUnix("unix", nil, addr); err == nil {
			conn.Close()
			break
		}
		i++
		if time.Duration(i)*step > d {
			return errors.Errorf("failed dialing: %s", address)
		}
		time.Sleep(step)
	}
	return nil
}
