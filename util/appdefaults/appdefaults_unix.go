// +build !windows

package appdefaults

const (
	Address = "unix:///run/buildkit/buildkitd.sock"
	Root    = "/var/lib/buildkit"
)
