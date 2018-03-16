package network

import (
	cni "github.com/containerd/go-cni"
)

const (
	defaultIfName        = "eth"
	defaultPluginConfDir = "/etc/cni/net.d"
	defaultPluginDir     = "/opt/cni/bin"
)

// InitCNI initializes the CNI and return CNI handle.
func InitCNI(pluginConfDir string, pluginDir string) (cni.CNI, error) {

	if pluginConfDir == "" {
		pluginConfDir = defaultPluginConfDir
	}
	if pluginDir == "" {
		pluginDir = defaultPluginDir
	}

	cniHandle, err := cni.New(
		cni.WithMinNetworkCount(2),
		cni.WithPluginConfDir(pluginConfDir),
		cni.WithPluginDir([]string{pluginDir}),
		cni.WithInterfacePrefix((defaultIfName)))
	if err != nil {
		return nil, err
	}

	err = cniHandle.Load(cni.WithDefaultConf())

	return cniHandle, err
}
