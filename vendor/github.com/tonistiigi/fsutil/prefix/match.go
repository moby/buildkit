package prefix

import (
	"path/filepath"
	"strings"
)

func Match(pattern, name string) (bool, bool) {
	count := strings.Count(name, string(filepath.Separator))
	partial := false
	if strings.Count(pattern, string(filepath.Separator)) > count {
		pattern = trimUntilIndex(pattern, string(filepath.Separator), count)
		partial = true
	}
	m, _ := filepath.Match(pattern, name)
	return m, partial
}

func trimUntilIndex(str, sep string, count int) string {
	s := str
	i := 0
	c := 0
	for {
		idx := strings.Index(s, sep)
		s = s[idx+len(sep):]
		i += idx + len(sep)
		c++
		if c > count {
			return str[:i-len(sep)]
		}
	}
}
