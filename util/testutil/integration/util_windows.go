package integration

import (
	"os/exec"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/pkg/errors"
)

func waitSocket(address string, d time.Duration, cmd *exec.Cmd) error {
	address = strings.TrimPrefix(address, "npipe://")
	step := 50 * time.Millisecond
	i := 0

	for {
		if cmd != nil && cmd.ProcessState != nil {
			return errors.Errorf("process exited: %s", cmd.String())
		}

		if conn, err := winio.DialPipe(address, nil); err == nil {
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
