//go:build !windows

package gateway

import "net"

func createNPipeListener() net.Listener {
	return nil
}
