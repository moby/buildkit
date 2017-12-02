package client

import (
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

type Client struct {
	conn *grpc.ClientConn
}

type ClientOpt interface{}

// New returns a new buildkit client. Address can be empty for the system-default address.
func New(address string, opts ...ClientOpt) (*Client, error) {
	gopts := []grpc.DialOption{
		grpc.WithInsecure(),
		grpc.WithTimeout(30 * time.Second),
		grpc.WithDialer(dialer),
		grpc.FailOnNonTempDialError(true),
	}
	for _, o := range opts {
		if _, ok := o.(*withBlockOpt); ok {
			gopts = append(gopts, grpc.WithBlock(), grpc.FailOnNonTempDialError(true))
		}
	}
	if address == "" {
		address = appdefaults.Address
	}
	conn, err := grpc.Dial(address, gopts...)
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

func (c *Client) Close() error {
	return c.conn.Close()
}

type withBlockOpt struct{}

func WithBlock() ClientOpt {
	return &withBlockOpt{}
}
