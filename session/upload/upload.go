package upload

import (
	"context"
	"errors"
	io "io"
	"net/url"

	"github.com/moby/buildkit/session"
	pkgerrors "github.com/pkg/errors"
	"google.golang.org/grpc/metadata"
)

const (
	keyPath = "urlpath"
	keyHost = "urlhost"
)

func New(ctx context.Context, c session.Caller, url *url.URL) (*Upload, error) {
	opts := map[string][]string{
		keyPath: {url.Path},
		keyHost: {url.Host},
	}

	client := NewUploadClient(c.Conn())

	ctx = metadata.NewOutgoingContext(ctx, opts)

	cc, err := client.Pull(ctx)
	if err != nil {
		return nil, pkgerrors.WithStack(err)
	}

	return &Upload{cc: cc}, nil
}

type Upload struct {
	cc Upload_PullClient
}

func (u *Upload) WriteTo(w io.Writer) (int64, error) {
	var n int64
	for {
		var bm BytesMessage
		if err := u.cc.RecvMsg(&bm); err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, pkgerrors.WithStack(err)
		}
		nn, err := w.Write(bm.Data)
		n += int64(nn)
		if err != nil {
			return n, pkgerrors.WithStack(err)
		}
	}
}
