//go:build !windows

package local

func runWithPrivileges(fn func() error) error {
	return fn()
}
