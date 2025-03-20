package dockerui

import (
	"path"
	"strings"
)

type EntrypointFile struct {
	Filename string
	Language string
	Required bool
}

func (file EntrypointFile) RelativeTo(filename string) string {
	if path.IsAbs(file.Filename) {
		return strings.TrimPrefix(file.Filename, "/")
	}

	return path.Join(filename, "..", file.Filename)
}
