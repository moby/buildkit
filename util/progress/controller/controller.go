package controller

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
)

type Controller struct {
	count   int64
	started *time.Time
	writer  progress.Writer

	Digest        digest.Digest
	Name          string
	WriterFactory progress.WriterFactory
}

var _ progress.Controller = &Controller{}

func (c *Controller) Start(ctx context.Context) (context.Context, func(error)) {
	if atomic.AddInt64(&c.count, 1) == 1 {
		if c.started == nil {
			now := time.Now()
			c.started = &now
			c.writer, _, _ = c.WriterFactory(ctx)
		}

		if c.Digest != "" {
			c.writer.Write(c.Digest.String(), client.Vertex{
				Digest:  c.Digest,
				Name:    c.Name,
				Started: c.started,
			})
		}
	}
	return progress.WithProgress(ctx, c.writer), func(err error) {
		if atomic.AddInt64(&c.count, -1) == 0 {
			now := time.Now()
			var errString string
			if err != nil {
				errString = err.Error()
			}
			if c.Digest != "" {
				c.writer.Write(c.Digest.String(), client.Vertex{
					Digest:    c.Digest,
					Name:      c.Name,
					Started:   c.started,
					Completed: &now,
					Error:     errString,
				})
			}
			c.writer.Close()
		}
	}
}

func (c *Controller) Status(id string, action string) func() {
	start := time.Now()
	c.writer.Write(id, progress.Status{
		Action:  action,
		Started: &start,
	})
	return func() {
		complete := time.Now()
		c.writer.Write(id, progress.Status{
			Action:    action,
			Started:   &start,
			Completed: &complete,
		})
	}
}
