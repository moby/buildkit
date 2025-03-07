package workers

import (
	"os"
	"slices"
	"strings"
)

type backend struct {
	address             string
	dockerAddress       string
	containerdAddress   string
	debugAddress        string
	rootless            bool
	netnsDetached       bool
	snapshotter         string
	extraEnv            []string
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

func (b backend) DebugAddress() string {
	return b.debugAddress
}

func (b backend) Rootless() bool {
	return b.rootless
}

func (b backend) NetNSDetached() bool {
	return b.netnsDetached
}

func (b backend) Snapshotter() string {
	return b.snapshotter
}

func (b backend) ExtraEnv() []string {
	return b.extraEnv
}

func (b backend) Supports(feature string) bool {
	if enabledFeatures := os.Getenv("BUILDKIT_TEST_ENABLE_FEATURES"); enabledFeatures != "" {
		if slices.Contains(strings.Split(enabledFeatures, ","), feature) {
			return true
		}
	}
	if disabledFeatures := os.Getenv("BUILDKIT_TEST_DISABLE_FEATURES"); disabledFeatures != "" {
		if slices.Contains(strings.Split(disabledFeatures, ","), feature) {
			return false
		}
	}
	return !slices.Contains(b.unsupportedFeatures, feature)
}
