//go:build !go1.18

package identify

import (
	"fmt"
	"runtime/debug"
)

func init() {
	bi, ok := debug.ReadBuildInfo()
	// ok will only be true if this is built as a dependency of another module
	if !ok {
		return
	}
	version := bi.Main.Version
	if version == "(devel)" {
		defaultUserAgent = bi.Main.Path
	} else {
		defaultUserAgent = fmt.Sprintf("%s@%s", bi.Main.Path, bi.Main.Version)
	}
}
