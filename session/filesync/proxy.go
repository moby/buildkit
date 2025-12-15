package filesync

import (
	"context"
	"fmt"
	"io"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type (
	ProxyDiffCopy     = proxySyncTarget[fstypes.Packet]
	ProxyStreamWriter = proxySyncTarget[BytesMessage]
)

// proxySyncTarget proxies messages send through the FileSend service to another
// session that exposes it.
type proxySyncTarget[Req any] struct {
	Caller     session.Caller
	ExporterID int
}

func (sp *proxySyncTarget[Req]) Register(server *grpc.Server) {
	RegisterFileSendServer(server, sp)
}

func (sp *proxySyncTarget[Req]) exportCtx(ctx context.Context) context.Context {
	opts, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		opts = make(map[string][]string)
	}
	if existingVal, ok := opts[keyExporterID]; ok {
		bklog.G(ctx).Warnf("overwriting grpc metadata key %q from value %+v to %+v", keyExporterID, existingVal, sp.ExporterID)
	}
	opts[keyExporterID] = []string{fmt.Sprint(sp.ExporterID)}
	ctx = metadata.NewOutgoingContext(ctx, opts)

	return ctx
}

func (sp *proxySyncTarget[Req]) DiffCopy(stream FileSend_DiffCopyServer) error {
	method := session.MethodURL(FileSend_ServiceDesc.ServiceName, "diffcopy")
	if !sp.Caller.Supports(method) {
		return errors.Errorf("method %s not supported by the client", method)
	}

	client := NewFileSendClient(sp.Caller.Conn())
	ctx := sp.exportCtx(stream.Context())

	eg, ctx := errgroup.WithContext(ctx)

	cc, err := client.DiffCopy(ctx)
	if err != nil {
		return err
	}

	eg.Go(func() error {
		for {
			var msg Req
			err := stream.RecvMsg(&msg)
			if errors.Is(err, io.EOF) {
				return cc.CloseSend()
			}
			if err != nil {
				return err
			}
			if err := cc.SendMsg(&msg); err != nil {
				return err
			}
		}
	})
	eg.Go(func() error {
		for {
			var msg Req
			err := cc.RecvMsg(&msg)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := stream.SendMsg(&msg); err != nil {
				return err
			}
		}
	})
	err = eg.Wait()
	if err != nil {
		cc.CloseSend()
	}
	return err
}
