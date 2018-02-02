package client

import (
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialer(address string, timeout time.Duration) (net.Conn, error) {
	address = strings.TrimPrefix(address, "npipe://")
	address = strings.Replace(address, "/", "\\", 0)
	return winio.DialPipe(address, &timeout)
}
