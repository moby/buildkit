package integration

import (
	"testing"
)

type Sandbox interface {
	Address() string
	PrintLogs(t *testing.T)
}

type Worker interface {
	New() (Sandbox, func() error, error)
	Name() string
}

type Test func(*testing.T, Sandbox)

var defaultWorkers []Worker

func register(w Worker) {
	defaultWorkers = append(defaultWorkers, w)
}

func List() []Worker {
	return defaultWorkers
}
