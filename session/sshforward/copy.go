package sshforward

import (
	"context"
	"io"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func Copy(ctx context.Context, conn io.ReadWriteCloser, stream grpc.Stream) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() (retErr error) {
		p := &BytesMessage{}
		for {
			if err := stream.RecvMsg(p); err != nil {
				conn.Close()
				if err == io.EOF {
					// indicates client performed CloseSend, but they may still be
					// reading data
					if closeWriter, ok := conn.(interface {
						CloseWrite() error
					}); ok {
						closeWriter.CloseWrite()
					} else {
						conn.Close()
					}
					return nil
				}
				return errors.WithStack(err)
			}
			select {
			case <-ctx.Done():
				conn.Close()
				return ctx.Err()
			default:
			}
			if _, err := conn.Write(p.Data); err != nil {
				conn.Close()
				return errors.WithStack(err)
			}
			p.Data = p.Data[:0]
		}
	})

	g.Go(func() (retErr error) {
		for {
			buf := make([]byte, 32*1024)
			n, err := conn.Read(buf)
			switch {
			case err == io.EOF:
				if closeStream != nil {
					closeStream()
				}
				return nil
			case err != nil:
				return errors.WithStack(err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			p := &BytesMessage{Data: buf[:n]}
			if err := stream.SendMsg(p); err != nil {
				return errors.WithStack(err)
			}
		}
	})

	return g.Wait()
}
