package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"
	controlapi "github.com/tonistiigi/buildkit_poc/api/services/control"
	"github.com/tonistiigi/buildkit_poc/client/llb"
	"golang.org/x/sync/errgroup"
)

func (c *Client) Solve(ctx context.Context, r io.Reader) error {
	def, err := llb.ReadFrom(r)
	if err != nil {
		return errors.Wrap(err, "failed to parse input")
	}

	if len(def) == 0 {
		return errors.New("invalid empty definition")
	}

	ref := generateID()
	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		_, err = c.controlClient().Solve(ctx, &controlapi.SolveRequest{
			Ref:        ref,
			Definition: def,
		})
		if err != nil {
			return errors.Wrap(err, "failed to solve")
		}
		return nil
	})

	eg.Go(func() error {
		stream, err := c.controlClient().Status(ctx, &controlapi.StatusRequest{
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
			logrus.Debugf("status: %+v", resp)
		}
	})

	return eg.Wait()
}

func generateID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
