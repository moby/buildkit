// +build !containerd

package client

func setupContainerd() (func(), error) {
	return func() {}, nil
}
