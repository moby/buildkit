package exporterprovider

import (
	"context"

	"github.com/moby/buildkit/session/exporter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Callback func(ctx context.Context, md map[string][]byte, refs []string) ([]*exporter.ExporterRequest, error)

type FinalizeCallback func(ctx context.Context, exporterResponse map[string]string) error

type Option func(*Exporter)

func WithFinalizeCallback(cb FinalizeCallback) Option {
	return func(e *Exporter) {
		e.finalize = cb
	}
}

func New(cb Callback, opts ...Option) *Exporter {
	e := &Exporter{cb: cb}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

type Exporter struct {
	cb       Callback
	finalize FinalizeCallback
}

func (e *Exporter) FinalizeExport(ctx context.Context, in *exporter.FinalizeExportRequest) (*exporter.FinalizeExportResponse, error) {
	if e.finalize == nil {
		return nil, status.Errorf(codes.Unimplemented, "no exporter finalize callback registered")
	}
	if err := e.finalize(ctx, in.ExporterResponse); err != nil {
		return nil, err
	}
	return &exporter.FinalizeExportResponse{}, nil
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
