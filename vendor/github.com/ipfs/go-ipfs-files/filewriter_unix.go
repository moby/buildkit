//go:build darwin || linux || netbsd || openbsd || freebsd || dragonfly
// +build darwin linux netbsd openbsd freebsd dragonfly

package files

import (
	"os"
	"strings"
	"syscall"
)

var invalidChars = `/` + "\x00"

func isValidFilename(filename string) bool {
	return !strings.ContainsAny(filename, invalidChars)
}

func createNewFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_EXCL|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0666)
}
