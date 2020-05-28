package controller

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress"
	"github.com/opencontainers/go-digest"
)

type Controller struct {
	count   int64
	started *time.Time

	Digest digest.Digest
	Name   string
	Writer progress.Writer
}

var _ progress.Controller = &Controller{}

func (c *Controller) Start(ctx context.Context) (context.Context, func(error)) {
	if c.Digest == "" {
		return progress.WithProgress(ctx, c.Writer), func(error) {}
	}

	if atomic.AddInt64(&c.count, 1) == 1 {
		if c.started == nil {
			now := time.Now()
			c.started = &now
		}
		c.Writer.Write(c.Digest.String(), client.Vertex{
			Digest:  c.Digest,
			Name:    c.Name,
			Started: c.started,
		})
	}
	return progress.WithProgress(ctx, c.Writer), func(err error) {
		if atomic.AddInt64(&c.count, -1) == 0 {
			now := time.Now()
			var errString string
			if err != nil {
				errString = err.Error()
			}
			c.Writer.Write(c.Digest.String(), client.Vertex{
				Digest:    c.Digest,
				Name:      c.Name,
				Started:   c.started,
				Completed: &now,
				Error:     errString,
			})
		}
	}
}

func (c *Controller) Status(id string, action string) func() {
	start := time.Now()
	c.Writer.Write(id, progress.Status{
		Action:  action,
		Started: &start,
	})
	return func() {
		complete := time.Now()
		c.Writer.Write(id, progress.Status{
			Action:    action,
			Started:   &start,
			Completed: &complete,
		})
	}
}
