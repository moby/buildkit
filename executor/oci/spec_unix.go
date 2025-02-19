//go:build !windows

package oci

// no effect for non-Windows
func getMountType(mType string) string {
	return mType
}

// no effect for non-Windows
func getCompleteSourcePath(p string) string {
	return p
}
