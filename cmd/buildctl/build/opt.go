package build

import (
	"github.com/sirupsen/logrus"
)

func ParseOpt(opts, legacyFrontendOpts []string) (map[string]string, error) {
	m := make(map[string]string)
	if len(legacyFrontendOpts) > 0 {
		logrus.Warn("--frontend-opt <opt>=<optval> is deprecated. Please use --opt <opt>=<optval> instead.")
		legacy, err := attrMap(legacyFrontendOpts)
		if err != nil {
			return nil, err
		}
		for k, v := range legacy {
			m[k] = v
		}
	}
	modern, err := attrMap(opts)
	if err != nil {
		return nil, err
	}
	for k, v := range modern {
		m[k] = v
	}
	return m, nil
}
