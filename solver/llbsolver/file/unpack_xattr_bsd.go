//go:build freebsd || netbsd

package file

import (
	"os"
	"strings"
)

// Symlink xattrs need a root-scoped, non-following implementation here.
func setRootSymlinkXattr(*os.Root, string, string, []byte) error {
	return nil
}

func setRootXattr(root *os.Root, file *os.File, name, key string, value []byte) error {
	// The x/sys shim only maps user.* and system.* to extattr namespaces.
	if !strings.HasPrefix(key, "user.") && !strings.HasPrefix(key, "system.") {
		return nil
	}
	return setRootXattrDefault(root, file, name, key, value)
}
