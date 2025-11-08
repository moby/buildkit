//go:build !windows

package integration

import (
	"fmt"
	"net"
)

var socketScheme = "unix://"

// abstracted function to handle pipe dialing on unix.
// some simplification has been made to discard
// laddr for unix -- left as nil.
func dialPipe(address string) (net.Conn, error) {
	addr, err := net.ResolveUnixAddr("unix", address)
	if err != nil {
		return nil, fmt.Errorf("failed resolving unix addr: %s: %w", address, err)
	}
	return net.DialUnix("unix", nil, addr)
}
