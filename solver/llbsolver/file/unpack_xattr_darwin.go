package file

import "os"

// Symlink xattrs need a root-scoped, non-following implementation here.
func setRootSymlinkXattr(*os.Root, string, string, []byte) error {
	return nil
}

func setRootXattr(root *os.Root, file *os.File, name, key string, value []byte) error {
	return setRootXattrDefault(root, file, name, key, value)
}
