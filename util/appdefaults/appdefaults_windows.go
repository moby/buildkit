package appdefaults

import (
	"os"
	"path/filepath"
)

const (
	Address          = "npipe:////./pipe/buildkitd"
	FrontendGRPCPipe = `\\.\pipe\buildkit-frontend-bridge`
)

var (
	Root                 = filepath.Join(os.Getenv("ProgramData"), "buildkitd", ".buildstate")
	ConfigDir            = filepath.Join(os.Getenv("ProgramData"), "buildkitd")
	DefaultCNIBinDir     = filepath.Join(ConfigDir, "bin")
	DefaultCNIConfigPath = filepath.Join(ConfigDir, "cni.json")
	CustomFrontends      = map[string]struct{}{"customfrontend": {}}
)

var (
	UserCNIConfigPath = DefaultCNIConfigPath
	CDISpecDirs       = []string{filepath.Join(os.Getenv("ProgramData"), "buildkitd", "cdi")}
)

func UserAddress() string {
	return Address
}

func EnsureUserAddressDir() error {
	return nil
}

func UserRoot() string {
	return Root
}

func UserConfigDir() string {
	return ConfigDir
}

func TraceSocketPath(inUserNS bool) string {
	return `\\.\pipe\buildkit-otel-grpc`
}

func AllowedCustomFrontends() map[string]struct{} {
	return CustomFrontends
}
