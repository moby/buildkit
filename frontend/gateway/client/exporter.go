package client

import (
	"context"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	contentproxy "github.com/containerd/containerd/v2/core/content/proxy"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session/filesync"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc"
)

type ExportHandle struct {
	Target exptypes.ExporterTarget
	Conn   *grpc.ClientConn
}

func (e *ExportHandle) ContentStore() content.Store {
	return contentproxy.NewContentStore(e.Conn)
}

func (e *ExportHandle) SendFile(ctx context.Context) (io.WriteCloser, error) {
	if e.Target != exptypes.ExporterTargetFile {
		return nil, errors.Errorf("invalid target for file export: %s", e.Target)
	}

	client := filesync.NewFileSendClient(e.Conn)
	cc, err := client.DiffCopy(ctx)
	if err != nil {
		return nil, err
	}
	return filesync.NewStreamWriter(cc), nil
}

func (e *ExportHandle) SendFS(ctx context.Context, fs fsutil.FS) error {
	if e.Target != exptypes.ExporterTargetDirectory {
		return errors.Errorf("invalid target for directory export: %s", e.Target)
	}

	client := filesync.NewFileSendClient(e.Conn)
	cc, err := client.DiffCopy(ctx)
	if err != nil {
		return err
	}
	return errors.WithStack(fsutil.Send(cc.Context(), cc, fs, nil))
}
