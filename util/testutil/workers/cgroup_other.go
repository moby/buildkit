//go:build !linux

package workers

func delegateCgroup(_, _ int) (string, func() error, error) {
	return "", nil, nil
}
