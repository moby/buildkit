package boltutil

import (
	"errors"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/windows"
)

func (d *DB) installCompacted(dst *bolt.DB) (bool, error) {
	// Windows requires both mapped files to be closed before replacement.
	tmp := dst.Path()
	if err := dst.Close(); err != nil {
		return false, err
	}
	var replaced bool
	cause := d.bdb.Close()
	if cause == nil {
		cause = replaceFile(tmp, d.path)
		replaced = cause == nil
	}
	bdb, err := bolt.Open(d.path, d.mode, d.opts)
	if err == nil {
		d.bdb = bdb
	}
	return replaced, errors.Join(cause, err)
}

func replaceFile(from, to string) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
