package client

import (
	"time"

	"github.com/pkg/errors"
	controlapi "github.com/tonistiigi/buildkit_poc/api/services/control"
	"google.golang.org/grpc"
)

type Client struct {
	conn *grpc.ClientConn
}

// New returns a new buildkit client
func New(address string) (*Client, error) {
	gopts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithInsecure(),
		grpc.WithTimeout(100 * time.Second),
		grpc.WithDialer(dialer),
		grpc.FailOnNonTempDialError(true),
	}
	conn, err := grpc.Dial(dialAddress(address), gopts...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to dial %q . make sure buildd is running", address)
	}
	c := &Client{
		conn: conn,
	}
	return c, nil
}

func (c *Client) controlClient() controlapi.ControlClient {
	return controlapi.NewControlClient(c.conn)
}
