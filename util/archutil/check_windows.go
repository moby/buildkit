//go:build windows

package archutil

import (
	"errors"
)

func check(string, string) (string, error) {
	return "", errors.New("binfmt is not supported on Windows")
}
