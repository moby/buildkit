package multiplatform

import (
	"strconv"

	"github.com/pkg/errors"
)

const (
	frontendMultiPlatform    = "multi-platform"
	frontendMultiPlatformArg = "build-arg:BUILDKIT_MULTI_PLATFORM"

	KeyMultiPlatform = "multi-platform"
)

func ParseBuildArgs(opt map[string]string) (string, bool) {
	if v, ok := opt[frontendMultiPlatform]; ok {
		return v, true
	}
	if v, ok := opt[frontendMultiPlatformArg]; ok {
		return v, true
	}
	return "", false
}

func ParseExporterAttrs(opt map[string]string) (*bool, map[string]string, error) {
	rest := make(map[string]string, len(opt))

	var multiPlatform *bool

	for k, v := range opt {
		switch k {
		case KeyMultiPlatform:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, nil, errors.Errorf("invalid boolean value %s", v)
			}
			multiPlatform = &b
		default:
			rest[k] = v
		}
	}

	return multiPlatform, rest, nil
}
