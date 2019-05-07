// +build !windows

package sysprocattr

import (
	"os/exec"
	"syscall"
)

func Setsid(cmd *exec.Cmd, v bool) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = v
}
