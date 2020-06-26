package ttyproxy

import (
	context "context"
	io "io"

	"github.com/creack/pty"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	grpc "google.golang.org/grpc"
)

type TTYIO struct {
	Stdin  io.ReadCloser
	Stdout io.WriteCloser
	Stderr io.WriteCloser
}

func Attach(ctx context.Context, conn *grpc.ClientConn) (*TTYIO, func() error, error) {
	pts, ptm, err := pty.Open()
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	ctx, cancel := context.WithCancel(ctx)
	eg, ctx := errgroup.WithContext(ctx)

	client := NewTTYProxyClient(conn)
	_, err = client.Acquire(ctx, &AcquireRequest{})
	if err != nil {
		cancel()
		return nil, nil, errors.WithStack(err)
	}

	defer func() {
		client.Release(ctx, &ReleaseRequest{})
	}()

	dataStream, err := client.Proxy(ctx)
	if err != nil {
		cancel()
		return nil, nil, errors.WithStack(err)
	}

	resizeStream, err := client.Resize(ctx, &ResizeRequest{})
	if err != nil {
		cancel()
		return nil, nil, errors.WithStack(err)
	}

	eg.Go(func() error {
		<-ctx.Done()
		return ctx.Err()
	})

	eg.Go(func() error {
		for {
			size, err := resizeStream.Recv()
			if err != nil {
				if err == io.EOF {
					resizeStream.CloseSend()
					return nil
				}
				return errors.WithStack(err)
			}
			err = pty.Setsize(ptm, &pty.Winsize{
				Rows: uint16(size.Rows),
				Cols: uint16(size.Columns),
				X:    uint16(size.X),
				Y:    uint16(size.Y),
			})
			if err != nil {
				return errors.WithStack(err)
			}
		}
	})

	eg.Go(func() error {
		for {
			input, err := dataStream.Recv()
			if err != nil {
				if err == io.EOF {
					dataStream.CloseSend()
					return nil
				}
				return errors.WithStack(err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			_, err = ptm.Write(input.Data)
			if err != nil {
				return errors.WithStack(err)
			}
		}
	})

	eg.Go(func() error {
		dest := &stdoutWriter{dataStream}
		_, err := io.Copy(dest, ptm)
		return err
	})

	return &TTYIO{
			Stdin:  pts,
			Stdout: pts,
			Stderr: pts,
		}, func() error {
			err := pts.Close()
			if err != nil {
				return errors.WithStack(err)
			}

			err = ptm.Close()
			if err != nil {
				return errors.WithStack(err)
			}

			cancel()

			err = eg.Wait()
			if err != nil {
				return errors.WithStack(err)
			}
			return nil
		}, nil
}

type stdoutWriter struct {
	stream TTYProxy_ProxyClient
}

func (w *stdoutWriter) Write(msg []byte) (int, error) {
	err := w.stream.Send(&BytesMessage{
		Data: msg,
	})
	return len(msg), err
}
