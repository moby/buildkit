package system

import (
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pkg/errors"
)

// DefaultPathEnvUnix is unix style list of directories to search for
// executables. Each directory is separated from the next by a colon
// ':' character .
const DefaultPathEnvUnix = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// DefaultPathEnvWindows is windows style list of directories to search for
// executables. Each directory is separated from the next by a colon
// ';' character .
const DefaultPathEnvWindows = "c:\\Windows\\System32;c:\\Windows"

func DefaultPathEnv(os string) string {
	if os == "windows" {
		return DefaultPathEnvWindows
	}
	return DefaultPathEnvUnix
}

// NormalizePath functions similarly to filepath.Join(), but with a few distinctions:
//   - use the platform flag to decide how we clean the path
//   - always return an absolute path
//   - always strip drive letters for Windows paths
//   - optionally keep the trailing slashes on paths
//   - paths are returned using forward slashes
func NormalizePath(parent, path, platform string, keepSlash bool) (string, error) {
	origPath := path

	if parent == "" {
		parent = "/"
	}

	var err error
	parent, err = CheckSystemDriveAndRemoveDriveLetter(parent, platform)
	if err != nil {
		return "", errors.Wrap(err, "removing drive letter")
	}

	if !IsAbs(parent, platform) {
		parent = filepath.Join("/", parent)
	}

	if path == "" {
		// New workdir is empty. Use the "current" workdir. It should already
		// be an absolute path.
		path = parent
	}

	path, err = CheckSystemDriveAndRemoveDriveLetter(path, platform)
	if err != nil {
		return "", errors.Wrap(err, "removing drive letter")
	}

	if !IsAbs(path, platform) {
		// The new WD is relative. Join it to the previous WD.
		path = filepath.Join(parent, path)
	}
	path = filepath.ToSlash(path)

	if keepSlash {
		if strings.HasSuffix(origPath, "/") && !strings.HasSuffix(path, "/") {
			path += "/"
		} else if strings.HasSuffix(origPath, "/.") {
			if path != "/" {
				path += "/"
			}
			path += "."
		}
	}

	return path, nil
}

// NormalizeWorkdir will return a normalized version of the new workdir, given
// the currently configured workdir and the desired new workdir. When setting a
// new relative workdir, it will be joined to the previous workdir or default to
// the root folder.
// On Windows we remove the drive letter and convert the path delimiter to "\".
// Paths that begin with os.PathSeparator are considered absolute even on Windows.
func NormalizeWorkdir(current, wd string, platform string) (string, error) {
	wd, err := NormalizePath(current, wd, platform, false)
	if err != nil {
		return "", errors.Wrap(err, "normalizing working directory")
	}

	// Make sure we use the platform specific path separator. HCS does not like forward
	// slashes in CWD.
	if platform == "windows" {
		return strings.ReplaceAll(wd, "/", `\`), nil
	}
	// We're normalizing the path for a unix system, so convert to slash.
	return filepath.ToSlash(wd), nil
}

// IsAbs returns a boolean value indicating whether or not the path
// is absolute. On Linux, this is just a wrapper for filepath.IsAbs().
// On Windows, we strip away the drive letter (if any), clean the path,
// and check whether or not the path starts with a filepath.Separator.
// This function is meant to check if a path is absolute, in the context
// of a COPY, ADD or WORKDIR, which have their root set in the mount point
// of the writable layer we are mutating. The filepath.IsAbs() function on
// Windows will not work in these scenatios, as it will return true for paths
// that:
//   - Begin with drive letter (DOS style paths)
//   - Are volume paths \\?\Volume{UUID}
//   - Are UNC paths
//   - Are a reserved name (COM, AUX, NUL, etc)
func IsAbs(pth, platform string) bool {
	if platform == "" {
		platform = runtime.GOOS
	}
	cleanedPath, err := CheckSystemDriveAndRemoveDriveLetter(pth, platform)
	if err != nil {
		return false
	}
	cleanedPath = filepath.ToSlash(cleanedPath)
	// We stripped any potential drive letter and converted any backslashes to
	// forward slashes. We can safely use path.IsAbs() for both Windows and Linux.
	return path.IsAbs(cleanedPath)
}

// CheckSystemDriveAndRemoveDriveLetter verifies and manipulates a Windows path.
// For linux, this is a no-op.
//
// This is used, for example, when validating a user provided path in docker cp.
// If a drive letter is supplied, it must be the system drive. The drive letter
// is always removed. It also converts any backslash to forward slash. The conversion
// to OS specific separator should happen as late as possible (ie: before passing the
// value to the function that will actually use it). Paths are parsed and code paths are
// triggered starting with the client and all the way down to calling into the runtime
// environment. The client may run on a foreign OS from the one the build will be triggered
// (Windows clients connecting to Linux or vice versa).
// Keeping the file separator consistent until the last moment is desirable.
//
// We need the Windows path without the drive letter so that it can ultimately be concatenated with
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
func CheckSystemDriveAndRemoveDriveLetter(path string, platform string) (string, error) {
	if platform == "" {
		platform = runtime.GOOS
	}

	if platform != "windows" {
		return path, nil
	}

	if len(path) == 2 && string(path[1]) == ":" {
		return "", errors.Errorf("No relative path specified in %q", path)
	}

	// UNC paths should error out
	if len(path) >= 2 && filepath.ToSlash(path[:2]) == "//" {
		return "", errors.Errorf("UNC paths are not supported")
	}

	parts := strings.SplitN(path, ":", 2)
	// Path does not have a drive letter. Just return it.
	if len(parts) < 2 {
		return filepath.ToSlash(filepath.Clean(path)), nil
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
	return filepath.ToSlash(filepath.Clean(parts[1])), nil
}
