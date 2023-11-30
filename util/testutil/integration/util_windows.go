package integration

import (
	"net"

	"github.com/Microsoft/go-winio"
)

var socketScheme = "npipe://"

// abstracted function to handle pipe dialing on windows.
// some simplification has been made to discard timeout param.
func dialPipe(address string) (net.Conn, error) {
	return winio.DialPipe(address, nil)
}
