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
	// On Windows, filepath.IsAbs() will return true for paths that:
	//   * Begin with drive letter (DOS style paths)
	//   * Are volume paths \\?\Volume{UUID}
	//   * Are UNC paths
	//   * Are a reserved name (COM, AUX, NUL, etc)
	// Using filepath.IsAbs() here, will yield undesirable results, as a proper, valid absolute
	// path that is a UNC path (for example), will skip any !filepath.IsAbs() check, and reach
	// the end of this function, where we would simply strip the first two characters (which in
	// a UNC path are "\\") and return a string that makes no sense. In the context of what we're
	// using this function for, we only care about DOS style absolute paths.
	//
	// On Windows, the ":" character is illegal inside a folder or fie name, mainly because
	// it's used to delimit drive letters from the rest of the path that resides inside that
	// DOS path.
	// Thus, it's easier to simply split the path by ":", and regard the first element as the drive
	// letter, and the second one as the path.
	// NOTE: This function only cares about DOS style absolute paths. If the path is any other kind of
	// absolute path, we will return it here, without mangling it, aside from cleaning it and
	// making sure the windows path delimiter is used (which should be fine).
	//
	// By not mangling the path, we allow any subsequent check to determine if the path is suitable for
	// whatever need exists.
	// Alternatively, we could further validate the path and error out here, but that seems outside the
	// scope of what this function should do.
	parts := strings.SplitN(path, ":", 2)
	if len(parts) == 1 || len(path) < 2 {
		return filepath.FromSlash(filepath.Clean(path)), nil
	}
	if !strings.EqualFold(parts[0], "c") {
		return "", errors.New("The specified path is not on the system drive (C:)")
	}

	// Apparently, a path of the form F:somepath, seems to be valid in a cmd shell:
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
	// Join with "/" and return it
	return filepath.Join("/", filepath.Clean(parts[1])), nil
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
