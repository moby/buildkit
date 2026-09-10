//go:build !linux

package executor

// InjectProxyCA is only implemented for Linux rootfs layouts.
func InjectProxyCA(string, []byte) (func() error, error) {
	return func() error { return nil }, nil
}
