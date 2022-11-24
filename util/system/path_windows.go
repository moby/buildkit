//go:build windows
// +build windows

package system

import (
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// CheckSystemDriveAndRemoveDriveLetter verifies and manipulates a Windows path.
// This is used, for example, when validating a user provided path in docker cp.
// If a drive letter is supplied, it must be the system drive. The drive letter
// is always removed. Also, it translates it to OS semantics (IOW / to \). We
// need the path in this syntax so that it can ultimately be contatenated with
// a Windows long-path which doesn't support drive-letters. Examples:
// C:			--> Fail
// C:\			--> \
// a			--> a
// /a			--> \a
// d:\			--> Fail
func CheckSystemDriveAndRemoveDriveLetter(path string) (string, error) {
	if len(path) == 2 && string(path[1]) == ":" {
		return "", errors.Errorf("No relative path specified in %q", path)
	}
	if !filepath.IsAbs(path) || len(path) < 2 {
		return filepath.FromSlash(filepath.Clean(path)), nil
	}
	if string(path[1]) == ":" && !strings.EqualFold(string(path[0]), "c") {
		return "", errors.New("The specified path is not on the system drive (C:)")
	}
	return filepath.FromSlash(filepath.Clean(path[2:])), nil
}

func IsAbs(pth string) bool {
	cleanedPath, err := CheckSystemDriveAndRemoveDriveLetter(pth)
	if err != nil {
		return false
	}
	cleanedPath = filepath.FromSlash(cleanedPath)
	if cleanedPath != "" && cleanedPath[0] == filepath.Separator {
		return true
	}
	return false
}
