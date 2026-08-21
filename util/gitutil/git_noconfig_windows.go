//go:build windows

package gitutil

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	noConfigOnce sync.Once
	noConfigDir  string
	noConfigFile string
)

func initNoConfig() {
	// On Windows ARM64, git cannot use the NUL device as HOME or a config
	// path ("fatal: unable to access 'NUL': Invalid argument"). Create a
	// temporary empty directory for HOME (so ~/.gitconfig is absent) and an
	// empty file for GIT_CONFIG_GLOBAL.
	dir, err := os.MkdirTemp("", "buildkit-git-noconfig")
	if err != nil {
		// Fallback: use os.DevNull (works on AMD64, may fail on ARM64).
		noConfigDir = os.DevNull
		noConfigFile = os.DevNull
		return
	}
	noConfigDir = dir
	noConfigFile = filepath.Join(dir, "empty-gitconfig")
	// Best-effort create; an empty file is a valid (empty) gitconfig.
	os.WriteFile(noConfigFile, nil, 0o600)
}

// noConfigHome returns a path to use for HOME that prevents git from
// reading user-level configuration.
func noConfigHome() string {
	noConfigOnce.Do(initNoConfig)
	return noConfigDir
}

// noConfigGlobal returns a path to use for GIT_CONFIG_GLOBAL that
// prevents git from reading the global gitconfig.
func noConfigGlobal() string {
	noConfigOnce.Do(initNoConfig)
	return noConfigFile
}
