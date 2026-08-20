package build

import (
	"maps"
	"os"
	"strings"
)

const buildArgPrefix = "build-arg:"

func ParseOpt(opts []string) (map[string]string, error) {
	m := loadOptEnv()

	// A build arg given without a value, e.g. "build-arg:FOO", takes its value
	// from the client environment, matching "docker build --build-arg FOO".
	// Variables that are not set in the environment are left out entirely, so
	// the default from the Dockerfile still applies. Resolved args keep their
	// position in the list so that the last of two duplicates still wins.
	resolved := make([]string, 0, len(opts))
	for _, opt := range opts {
		if name, ok := strings.CutPrefix(opt, buildArgPrefix); ok && name != "" && !strings.Contains(name, "=") {
			v, ok := os.LookupEnv(name)
			if !ok {
				continue
			}
			opt += "=" + v
		}
		resolved = append(resolved, opt)
	}

	m2, err := attrMap(resolved)
	if err != nil {
		return nil, err
	}
	maps.Copy(m, m2)
	return m, nil
}
