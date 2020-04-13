// +build amd64

package mpatch

// Gets the jump function rewrite bytes
func getJumpFuncBytes(to uintptr) ([]byte, error) {
	return []byte{
		0x48, 0xBA,
		byte(to),
		byte(to >> 8),
		byte(to >> 16),
		byte(to >> 24),
		byte(to >> 32),
		byte(to >> 40),
		byte(to >> 48),
		byte(to >> 56),
		0xFF, 0x22,
	}, nil
}
