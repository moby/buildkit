package main

import (
	"runtime"
	"strconv"
	"strings"

	"github.com/docker/docker/pkg/idtools"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// parseBoolOrAuto returns (nil, nil) if s is "auto"
func parseBoolOrAuto(s string) (*bool, error) {
	if s == "" || strings.ToLower(s) == "auto" {
		return nil, nil
	}
	b, err := strconv.ParseBool(s)
	return &b, err
}

func parseIdentityMapping(str string) (*idtools.IdentityMapping, error) {
	if str == "" {
		return nil, nil
	}
	if runtime.GOOS != "linux" && str != "" {
		return nil, errors.Errorf("user namespaces are only supported on linux")
	}

	idparts := strings.Split(str, ":")
	if len(idparts) > 2 {
		return nil, errors.Errorf("invalid userns remap specification in %q", str)
	}

	username := idparts[0]
	groupname := username
	if len(idparts) == 2 {
		groupname = idparts[1]
	}

	logrus.Debugf("user namespaces: ID ranges will be mapped to subuid/subgid ranges of: %s:%s", username, groupname)

	mappings, err := idtools.NewIdentityMapping(username, groupname)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ID mappings")
	}
	return mappings, nil

}
