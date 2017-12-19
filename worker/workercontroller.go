package worker

import (
	"sync"

	"github.com/containerd/containerd/filters"
	"github.com/pkg/errors"
)

// Controller holds worker instances.
// Currently, only local workers are supported.
type Controller struct {
	mu sync.Mutex
	// TODO: define worker interface and support remote ones
	workers []Worker
}

// Add adds a local worker
func (c *Controller) Add(w Worker) error {
	c.mu.Lock()
	c.workers = append(c.workers, w)
	c.mu.Unlock()
	return nil
}

// List lists workers
func (c *Controller) List(filterStrings ...string) ([]Worker, error) {
	filter, err := filters.ParseAll(filterStrings...)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	allWorkers := c.workers
	c.mu.Unlock()
	var workers []Worker
	for _, w := range allWorkers {
		if filter.Match(adaptWorker(w)) {
			workers = append(workers, w)
		}
	}
	return workers, nil
}

// GetDefault returns the default local worker
func (c *Controller) GetDefault() (Worker, error) {
	var w Worker
	c.mu.Lock()
	if len(c.workers) > 0 {
		w = c.workers[0]
	}
	c.mu.Unlock()
	if w == nil {
		return nil, errors.New("no worker registered")
	}
	return w, nil
}

// TODO: add Get(Constraint) (*Worker, error)
