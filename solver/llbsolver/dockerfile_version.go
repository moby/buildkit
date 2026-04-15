package llbsolver

import (
	"strings"

	dockerfilebuilder "github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/version"
	"github.com/pkg/errors"
	"golang.org/x/mod/semver"
)

func builtinDockerfileVersion() (string, error) {
	return normalizeBuiltinDockerfileVersion(dockerfilebuilder.Version, version.Version)
}

func normalizeBuiltinDockerfileVersion(v, buildkitVersion string) (string, error) {
	v = strings.TrimSpace(v)
	parts := strings.Split(v, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return "", errors.Errorf("invalid dockerfile frontend version %q", v)
	}
	for _, part := range parts {
		if part == "" {
			return "", errors.Errorf("invalid dockerfile frontend version %q", v)
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return "", errors.Errorf("invalid dockerfile frontend version %q", v)
			}
		}
	}
	if len(parts) == 2 {
		v += ".0"
	}
	if !semver.IsValid(buildkitVersion) {
		return v + "-dev", nil
	}
	if prerelease := semver.Prerelease(buildkitVersion); prerelease != "" {
		return v + prerelease, nil
	}
	if semver.Build(buildkitVersion) != "" {
		return v + "-dev", nil
	}
	return v, nil
}
