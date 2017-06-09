package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"

	"github.com/pkg/errors"
	controlapi "github.com/tonistiigi/buildkit_poc/api/services/control"
	"github.com/tonistiigi/buildkit_poc/client/llb"
)

func (c *Client) Solve(ctx context.Context, r io.Reader) error {
	def, err := llb.ReadFrom(r)
	if err != nil {
		return errors.Wrap(err, "failed to parse input")
	}

	if len(def) == 0 {
		return errors.New("invalid empty definition")
	}

	_, err = c.controlClient().Solve(ctx, &controlapi.SolveRequest{
		Ref:        generateID(),
		Definition: def,
	})
	if err != nil {
		return errors.Wrap(err, "failed to solve")
	}
	return nil
}

func generateID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
