package workers

import (
	"os"
	"strings"
)

type backend struct {
	address             string
	dockerAddress       string
	containerdAddress   string
	rootless            bool
	snapshotter         string
	unsupportedFeatures []string
	isDockerd           bool
}

func (b backend) Address() string {
	return b.address
}

func (b backend) DockerAddress() string {
	return b.dockerAddress
}

func (b backend) ContainerdAddress() string {
	return b.containerdAddress
}

func (b backend) Rootless() bool {
	return b.rootless
}

func (b backend) Snapshotter() string {
	return b.snapshotter
}

func (b backend) Supports(feature string) bool {
	if enabledFeatures := os.Getenv("BUILDKIT_TEST_ENABLE_FEATURES"); enabledFeatures != "" {
		for _, enabledFeature := range strings.Split(enabledFeatures, ",") {
			if feature == enabledFeature {
				return true
			}
		}
	}
	if disabledFeatures := os.Getenv("BUILDKIT_TEST_DISABLE_FEATURES"); disabledFeatures != "" {
		for _, disabledFeature := range strings.Split(disabledFeatures, ",") {
			if feature == disabledFeature {
				return false
			}
		}
	}
	for _, unsupportedFeature := range b.unsupportedFeatures {
		if feature == unsupportedFeature {
			return false
		}
	}
	return true
}
