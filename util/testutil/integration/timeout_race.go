//go:build race

package integration

import "time"

// Allow extra time for race instrumentation in the tests and worker binaries.
const maxSandboxTimeout = 15 * time.Minute
