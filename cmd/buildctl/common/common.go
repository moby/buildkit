package common

import (
	"context"
	"net/url"
	"time"

	"github.com/moby/buildkit/client"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/urfave/cli"
)

// ResolveClient resolves a client from CLI args
func ResolveClient(c *cli.Context) (*client.Client, error) {
	serverName := c.GlobalString("tlsservername")
	if serverName == "" {
		// guess servername as hostname of target address
		uri, err := url.Parse(c.GlobalString("addr"))
		if err != nil {
			return nil, err
		}
		serverName = uri.Hostname()
	}
	caCert := c.GlobalString("tlscacert")
	cert := c.GlobalString("tlscert")
	key := c.GlobalString("tlskey")

	opts := []client.ClientOpt{client.WithFailFast()}

	ctx := CommandContext(c)

	if span := opentracing.SpanFromContext(ctx); span != nil {
		opts = append(opts, client.WithTracer(span.Tracer()))
	}

	if caCert != "" || cert != "" || key != "" {
		opts = append(opts, client.WithCredentials(serverName, caCert, cert, key))
	}

	timeout := time.Duration(c.GlobalInt("timeout"))
	ctx, cancel := context.WithTimeout(ctx, timeout*time.Second)
	defer cancel()

	return client.New(ctx, c.GlobalString("addr"), opts...)
}
