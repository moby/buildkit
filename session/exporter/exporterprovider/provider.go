package exporterprovider

import (
	"context"

	"github.com/moby/buildkit/session/exporter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Callback func(ctx context.Context, md map[string][]byte, refs []string) ([]*exporter.ExporterRequest, error)

func New(cb Callback) *Exporter {
	return &Exporter{
		cb: cb,
	}
}

type Exporter struct {
	cb Callback
}

func (e *Exporter) Register(server *grpc.Server) {
	exporter.RegisterExporterServer(server, e)
}

func (e *Exporter) FindExporters(ctx context.Context, in *exporter.FindExportersRequest) (*exporter.FindExportersResponse, error) {
	if e.cb == nil {
		return nil, status.Errorf(codes.Unavailable, "no exporter callback registered")
	}
	res, err := e.cb(ctx, in.Metadata, in.Refs)
	if err != nil {
		return nil, err
	}
	return &exporter.FindExportersResponse{
		Exporters: res,
	}, nil
}
