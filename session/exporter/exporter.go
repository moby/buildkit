package exporter

import (
	"context"

	"github.com/moby/buildkit/session"
	"github.com/pkg/errors"
)

func New(ctx context.Context, c session.Caller, result map[string][]byte) ([]*ExporterRequest, error) {
	client := NewExporterClient(c.Conn())

	res, err := client.FindExporters(ctx, &FindExportersRequest{
		Metadata: result,
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return res.Exporters, nil
}
