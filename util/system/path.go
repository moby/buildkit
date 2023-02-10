package system

import (
	"path/filepath"

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

// NormalizeWorkdir will return a normalized version of the new workdir, given
// the currently configured workdir and the desired new workdir. When setting a
// new relative workdir, it will be joined to the previous workdir or default to
// the root folder.
// On Windows we remove the drive letter and convert the path delimiter to "\".
// Paths that begin with os.PathSeparator are considered absolute even on Windows.
func NormalizeWorkdir(current, wd string) (string, error) {
	if current == "" {
		current = "/"
	}

	var err error
	current, err = CheckSystemDriveAndRemoveDriveLetter(current)
	if err != nil {
		return "", errors.Wrap(err, "removing drive letter")
	}

	current = filepath.FromSlash(current)
	if !IsAbs(current) {
		// Convert to absolute paths. We are normalizing the CWD.
		//
		// On Windows:
		// Paths that start with a / or \ are absolute paths relative to the current drive
		// letter. For these paths, filepath.IsAbs() will return false, so we use the IsAbs()
		// helper function in this package.
		//
		// Cases on Windows:
		//   /workdir --> \workdir
		//   \workdir --> \workdir
		//   workdir  --> \workdir
		//
		// The final path separator will be converted to forward slash.
		//
		// Cases on linux:
		//    workdir  --> /workdir
		current = filepath.Join("/", current)
	}

	if wd == "" {
		// New workdir is empty. Use the "current" workdir. It should already
		// be an absolute path.
		wd = current
	}

	wd, err = CheckSystemDriveAndRemoveDriveLetter(wd)
	if err != nil {
		return "", errors.Wrap(err, "removing drive letter")
	}

	wd = filepath.FromSlash(wd)
	if !IsAbs(wd) {
		// The new WD is relative. Join it to the previous WD.
		wd = filepath.Join(current, wd)
	}

	// Make sure we use the platform specific path separator. HCS does not like forward
	// slashes in CWD.
	return filepath.FromSlash(wd), nil
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
func IsAbs(pth string) bool {
	return isAbs(pth)
}
