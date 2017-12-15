package worker

import (
	"sync"

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

// GetAll returns all local workers
func (c *Controller) GetAll() []Worker {
	c.mu.Lock()
	workers := c.workers
	c.mu.Unlock()
	return workers
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
