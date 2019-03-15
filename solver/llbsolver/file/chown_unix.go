// +build !windows

package file

func fixRootDirectory(p string) string {
	return p
}
