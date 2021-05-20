// +build windows

package sshprovider

import (
	"net"
	"regexp"
	"strings"
	"syscall"

	"github.com/Microsoft/go-winio"
	"github.com/pkg/errors"
)

// Returns the Windows OpenSSH agent named pipe path, but
// only if the agent is running. Returns an error otherwise.
func getFallbackAgentPath() (string, error) {
	// Windows OpenSSH agent uses a named pipe rather
	// than a UNIX socket. These pipes do not play nice
	// with os.Stat (which tries to open its target), so
	// use a FindFirstFile syscall to check for existence.
	var fd syscall.Win32finddata

	path := `\\.\pipe\openssh-ssh-agent`
	pathPtr, _ := syscall.UTF16PtrFromString(path)
	handle, err := syscall.FindFirstFile(pathPtr, &fd)

	if err != nil {
		msg := "Windows OpenSSH agent not available at %s." +
			" Enable the SSH agent service or set SSH_AUTH_SOCK."
		return "", errors.Errorf(msg, path)
	}

	_ = syscall.CloseHandle(handle)

	return path, nil
}

// Returns true if the path references a named pipe.
func isWindowsPipePath(path string) bool {
	// If path matches \\*\pipe\* then it references a named pipe
	// and requires winio.DialPipe() rather than DialTimeout("unix").
	// Slashes and backslashes may be used interchangeably in the path.
	// Path separators may consist of multiple consecutive (back)slashes.
	pipePattern := strings.ReplaceAll("^[/]{2}[^/]+[/]+pipe[/]+", "[/]", `[\\/]`)
	ok, _ := regexp.MatchString(pipePattern, path)
	return ok
}

func parsePlatformSocketPath(path string) *socketDialer {
	if isWindowsPipePath(path) {
		return &socketDialer{path: path, dialer: windowsPipeDialer}
	}

	return nil
}

func windowsPipeDialer(path string) (net.Conn, error) {
	return winio.DialPipe(path, nil)
}
