package manager

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/docker/cli/cli/version"
)

const (
	audience = "https://hub.docker.com"
	tenant   = "login.docker.com"
	clientID = "L4v0dmlNBpYUjGGab0C2JtgTgXr1Qz4d"
)

func NewManager() *OAuthManager {
	cliVersion := strings.ReplaceAll(version.Version, ".", "_")
	options := OAuthManagerOptions{
		Audience:   audience,
		ClientID:   clientID,
		Tenant:     tenant,
		DeviceName: fmt.Sprintf("docker-cli:%s:%s-%s", cliVersion, runtime.GOOS, runtime.GOARCH),
	}
	return New(options)
}
