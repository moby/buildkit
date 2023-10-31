package export

import (
	"context"

	"github.com/moby/buildkit/session"
	"google.golang.org/grpc"
)

type FinalizeExportCallback func(map[string]string) (map[string]string, error)

var _ session.Attachable = (*FinalizeExportCallback)(nil)

func (finalizer FinalizeExportCallback) Register(server *grpc.Server) {
	RegisterExportCallbackServer(server, finalizer)
}

func (finalizer FinalizeExportCallback) Finalize(ctx context.Context, req *FinalizeRequest) (*FinalizeResponse, error) {
	resp, err := finalizer(req.ExporterResponse)
	return &FinalizeResponse{ExporterResponse: resp}, err
}

func Finalize(ctx context.Context, c session.Caller, exporterResponse map[string]string) (map[string]string, error) {
	if !c.Supports(session.MethodURL(_ExportCallback_serviceDesc.ServiceName, "finalize")) {
		return nil, nil
	}
	client := NewExportCallbackClient(c.Conn())
	resp, err := client.Finalize(ctx, &FinalizeRequest{ExporterResponse: exporterResponse})
	if resp.ExporterResponse == nil {
		resp.ExporterResponse = map[string]string{}
	}
	return resp.ExporterResponse, err
}
