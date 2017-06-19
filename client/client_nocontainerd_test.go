// +build !standalone

package client

func setupStandalone() (func(), error) {
	return func() {}, nil
}
