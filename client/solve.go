package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type SolveOpt interface{}

type withSourceOpConcurrency struct {
	concurrency int
}

func WithSourceOpConcurrency(concurrency int) SolveOpt {
	return &withSourceOpConcurrency{concurrency: concurrency}
}

type withExecOpConcurrency struct {
	concurrency int
}

func WithExecOpConcurrency(concurrency int) SolveOpt {
	return &withExecOpConcurrency{concurrency: concurrency}
}

func (c *Client) Solve(ctx context.Context, r io.Reader, statusChan chan *SolveStatus, opts ...SolveOpt) error {
	def, err := llb.ReadFrom(r)
	if err != nil {
		return errors.Wrap(err, "failed to parse input")
	}

	if len(def) == 0 {
		return errors.New("invalid empty definition")
	}
	sourceOpConcurrency := 0
	execOpConcurrency := 0
	for _, o := range opts {
		if x, ok := o.(*withSourceOpConcurrency); ok {
			sourceOpConcurrency = x.concurrency
		} else if x, ok := o.(*withExecOpConcurrency); ok {
			execOpConcurrency = x.concurrency
		}
	}

	ref := generateID()
	eg, ctx := errgroup.WithContext(ctx)

	statusContext, cancelStatus := context.WithCancel(context.Background())

	eg.Go(func() error {
		defer func() { // make sure the Status ends cleanly on build errors
			go func() {
				<-time.After(3 * time.Second)
				cancelStatus()
			}()
		}()
		_, err = c.controlClient().Solve(ctx, &controlapi.SolveRequest{
			Ref:                 ref,
			Definition:          def,
			SourceOpConcurrency: int64(sourceOpConcurrency),
			ExecOpConcurrency:   int64(execOpConcurrency),
		})
		if err != nil {
			return errors.Wrap(err, "failed to solve")
		}
		return nil
	})

	eg.Go(func() error {
		stream, err := c.controlClient().Status(statusContext, &controlapi.StatusRequest{
			Ref: ref,
		})
		if err != nil {
			return errors.Wrap(err, "failed to get status")
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return errors.Wrap(err, "failed to receive status")
			}
			s := SolveStatus{}
			for _, v := range resp.Vertexes {
				s.Vertexes = append(s.Vertexes, &Vertex{
					Digest:    v.Digest,
					Inputs:    v.Inputs,
					Name:      v.Name,
					Started:   v.Started,
					Completed: v.Completed,
					Error:     v.Error,
				})
			}
			for _, v := range resp.Statuses {
				s.Statuses = append(s.Statuses, &VertexStatus{
					ID:        v.ID,
					Vertex:    v.Vertex,
					Name:      v.Name,
					Total:     v.Total,
					Current:   v.Current,
					Timestamp: v.Timestamp,
					Started:   v.Started,
					Completed: v.Completed,
				})
			}
			for _, v := range resp.Logs {
				s.Logs = append(s.Logs, &VertexLog{
					Vertex:    v.Vertex,
					Stream:    int(v.Stream),
					Data:      v.Msg,
					Timestamp: v.Timestamp,
				})
			}
			if statusChan != nil {
				statusChan <- &s
			}
		}
	})

	defer func() {
		if statusChan != nil {
			close(statusChan)
		}
	}()

	return eg.Wait()
}

func generateID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
