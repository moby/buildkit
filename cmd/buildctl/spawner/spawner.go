package spawner

import (
	"time"
)

type Spawner interface {
	// Spawn spawns the daemon and return the address (unix://...)
	Spawn(timeout time.Duration) (string, error)
	// Close stops the spawned daemon.
	Close() error
}
