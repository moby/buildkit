package file

import (
	"os"
	"syscall"
)

// mkdirAll is forked os.MkdirAll
func mkdirAll(path string, perm os.FileMode, user *uidgid) error {
	// Fast path: if we can tell whether path is a directory or file, stop with success or error.
	dir, err := os.Stat(path)
	if err == nil {
		if dir.IsDir() {
			return nil
		}
		return &os.PathError{Op: "mkdir", Path: path, Err: syscall.ENOTDIR}
	}

	// Slow path: make sure parent exists and then call Mkdir for path.
	i := len(path)
	for i > 0 && os.IsPathSeparator(path[i-1]) { // Skip trailing path separator.
		i--
	}

	j := i
	for j > 0 && !os.IsPathSeparator(path[j-1]) { // Scan backward over element.
		j--
	}

	if j > 1 {
		// Create parent.
		err = mkdirAll(fixRootDirectory(path[:j-1]), perm, user)
		if err != nil {
			return err
		}
	}

	dir, err1 := os.Lstat(path)
	if err1 == nil && dir.IsDir() {
		return nil
	}

	// Parent now exists; invoke Mkdir and use its result.
	err = os.Mkdir(path, perm)
	if err != nil {
		// Handle arguments like "foo/." by
		// double-checking that directory doesn't exist.
		dir, err1 := os.Lstat(path)
		if err1 == nil && dir.IsDir() {
			return nil
		}
		return err
	}

	if user != nil {
		if err := os.Chown(path, user.uid, user.gid); err != nil {
			return err
		}
	}

	return nil
}
