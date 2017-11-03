package credentials

import (
	"os/exec"
)

// DetectDefaultStore return the default credentials store for the platform if
// the store executable is available.
func DetectDefaultStore(store string) string {
	// user defined or no default for platform
	if store != "" || defaultCredentialsStore == "" {
		return store
	}

	if _, err := exec.LookPath(remoteCredentialsPrefix + defaultCredentialsStore); err == nil {
		return defaultCredentialsStore
	}
	return ""
}
