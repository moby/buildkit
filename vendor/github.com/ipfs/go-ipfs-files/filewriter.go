package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrInvalidDirectoryEntry = errors.New("invalid directory entry name")
var ErrPathExistsOverwrite = errors.New("path already exists and overwriting is not allowed")

// WriteTo writes the given node to the local filesystem at fpath.
func WriteTo(nd Node, fpath string) error {
	if _, err := os.Lstat(fpath); err == nil {
		return ErrPathExistsOverwrite
	} else if !os.IsNotExist(err) {
		return err
	}
	switch nd := nd.(type) {
	case *Symlink:
		return os.Symlink(nd.Target, fpath)
	case File:
		f, err := createNewFile(fpath)
		defer f.Close()
		if err != nil {
			return err
		}
		_, err = io.Copy(f, nd)
		if err != nil {
			return err
		}
		return nil
	case Directory:
		err := os.Mkdir(fpath, 0777)
		if err != nil {
			return err
		}

		entries := nd.Entries()
		for entries.Next() {
			entryName := entries.Name()
			if entryName == "" ||
				entryName == "." ||
				entryName == ".." ||
				!isValidFilename(entryName) {
				return ErrInvalidDirectoryEntry
			}
			child := filepath.Join(fpath, entryName)
			if err := WriteTo(entries.Node(), child); err != nil {
				return err
			}
		}
		return entries.Err()
	default:
		return fmt.Errorf("file type %T at %q is not supported", nd, fpath)
	}
}
