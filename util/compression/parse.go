//go:build !nydus
// +build !nydus

package compression

func Parse(t string) (Type, error) {
	return parse(t)
}
