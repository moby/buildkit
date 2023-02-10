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
// need the path in this syntax so that it can ultimately be concatenated with
// a Windows long-path which doesn't support drive-letters. Examples:
// C:			--> Fail
// C:somepath   --> somepath // This is a relative path to the CWD set for that drive letter
// C:\			--> \
// a			--> a
// /a			--> \a
// d:\			--> Fail
//
// UNC paths can refer to multiple types of paths. From local filesystem paths,
// to remote filesystems like SMB or named pipes.
// There is no sane way to support this without adding a lot of complexity
// which I am not sure is worth it.
// \\.\C$\a     --> Fail
func CheckSystemDriveAndRemoveDriveLetter(path string) (string, error) {
	if len(path) == 2 && string(path[1]) == ":" {
		return "", errors.Errorf("No relative path specified in %q", path)
	}

	// UNC paths should error out
	if len(path) >= 2 && filepath.ToSlash(path[:2]) == "//" {
		return "", errors.Errorf("UNC paths are not supported")
	}

	// Since this function expects a DOS path which may or may not contain a drive
	// letter, we can split by ":". The only Windows paths where a ":" is present are
	// DOS paths. It should be safe to split using that as a delimiter and return the
	// result after we validate.
	parts := strings.Split(path, ":")
	if len(parts) > 2 {
		return "", errors.Errorf("invalid path: %q", path)
	}

	// Path does not have a drive letter. Just return it.
	if len(parts) < 2 {
		return filepath.Clean(filepath.FromSlash(path)), nil
	}

	// We expect all paths to be in C:
	if !strings.EqualFold(parts[0], "c") {
		return "", errors.New("The specified path is not on the system drive (C:)")
	}

	// A path of the form F:somepath, is a path that is relative CWD set for a particular
	// drive letter. See:
	// https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file#fully-qualified-vs-relative-paths
	//
	// C:\>mkdir F:somepath
	// C:\>dir F:\
	// Volume in drive F is New Volume
	// Volume Serial Number is 86E5-AB64
	//
	// Directory of F:\
	//
	// 11/27/2022  02:22 PM    <DIR>          somepath
	// 			0 File(s)              0 bytes
	// 			1 Dir(s)   1,052,876,800 bytes free
	//
	// We must return the second element of the split path, as is, without attempting to convert
	// it to an absolute path. We have no knowledge of the CWD; that is treated elsewhere.
	return filepath.Clean(parts[1]), nil
}

func isAbs(pth string) bool {
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
