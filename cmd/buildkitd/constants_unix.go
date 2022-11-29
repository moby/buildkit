//go:build !windows
// +build !windows

package main

const (
	defaultContainerdAddress = "/run/containerd/containerd.sock"
	defaultCNIBinDir         = "/opt/cni/bin"
	defaultCNIConfigPath     = "/etc/buildkit/cni.json"
)
