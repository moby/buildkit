package integration

import (
	"net"

	"github.com/Microsoft/go-winio"

	// include npipe connhelper for windows tests
	_ "github.com/moby/buildkit/client/connhelper/npipe"
)

var socketScheme = "npipe://"

var windowsImagesMirrorMap = map[string]string{
	"nanoserver:latest": "mcr.microsoft.com/windows/nanoserver:ltsc2022",
	"servercore:latest": "mcr.microsoft.com/windows/servercore:ltsc2022",
	"busybox:latest":    "registry.k8s.io/e2e-test-images/busybox:1.29-2",
}

// abstracted function to handle pipe dialing on windows.
// some simplification has been made to discard timeout param.
func dialPipe(address string) (net.Conn, error) {
	return winio.DialPipe(address, nil)
}
