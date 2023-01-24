//go:build go1.18

package identify

import (
	"fmt"
	"runtime/debug"
)

func init() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	version := bi.Main.Version
	// version will only be non-empty if built as a dependency of another module
	if version == "" {
		return
	}

	if version != "(devel)" {
		defaultUserAgent = fmt.Sprintf("%s@%s", bi.Main.Path, bi.Main.Version)
		return
	}

	var revision string
	var dirty bool
	for _, bs := range bi.Settings {
		switch bs.Key {
		case "vcs.revision":
			revision = bs.Value
			if len(revision) > 9 {
				revision = revision[:9]
			}
		case "vcs.modified":
			if bs.Value == "true" {
				dirty = true
			}
		}
	}
	defaultUserAgent = fmt.Sprintf("%s@%s", bi.Main.Path, revision)
	if dirty {
		defaultUserAgent += "-dirty"
	}
}
