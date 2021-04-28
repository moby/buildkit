package prefix

import (
	"path"
	"path/filepath"
	"strings"
)

func Match(pattern, name string, slashSeparator bool) (bool, bool) {
	separator := filepath.Separator
	if slashSeparator {
		separator = '/'
	}
	count := strings.Count(name, string(separator))
	partial := false
	if strings.Count(pattern, string(separator)) > count {
		pattern = trimUntilIndex(pattern, string(separator), count)
		partial = true
	}
	var m bool
	if slashSeparator {
		m, _ = path.Match(pattern, name)
	} else {
		m, _ = filepath.Match(pattern, name)
	}
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
