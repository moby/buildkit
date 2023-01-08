package pnet

import "os"

// EnvKey defines environment variable name for forcing usage of PNet in libp2p
// When environment variable of this name is set to "1" the ForcePrivateNetwork
// variable will be set to true.
const EnvKey = "LIBP2P_FORCE_PNET"

// ForcePrivateNetwork is boolean variable that forces usage of PNet in libp2p
// Setting this variable to true or setting LIBP2P_FORCE_PNET environment variable
// to true will make libp2p to require private network protector.
// If no network protector is provided and this variable is set to true libp2p will
// refuse to connect.
var ForcePrivateNetwork = false

func init() {
	ForcePrivateNetwork = os.Getenv(EnvKey) == "1"
}
