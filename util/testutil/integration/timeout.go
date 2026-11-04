//go:build !race

package integration

import "time"

const maxSandboxTimeout = 5 * time.Minute
