package ttyprovider

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/ttyproxy"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
)

type FdReader interface {
	Fd() uintptr
	Read(p []byte) (n int, err error)
}

// NewTTYProvider creates a session provider that allows access to the client tty
func NewTTYProvider(stdin FdReader) (session.Attachable, error) {
	return &ttyProvider{stdin: stdin}, nil
}

type ttyProvider struct {
	stdin     FdReader
	mux       sync.Mutex
	origState *terminal.State
}

func (tp *ttyProvider) Register(server *grpc.Server) {
	ttyproxy.RegisterTTYProxyServer(server, tp)
}

func (tp *ttyProvider) Acquire(ctx context.Context, _ *ttyproxy.AcquireRequest) (*ttyproxy.AcquireResponse, error) {
	tp.mux.Lock()
	var err error
	tp.origState, err = terminal.MakeRaw(int(tp.stdin.Fd()))
	if err != nil {
		tp.mux.Unlock()
	}
	return &ttyproxy.AcquireResponse{}, err
}

func (tp *ttyProvider) Release(ctx context.Context, _ *ttyproxy.ReleaseRequest) (*ttyproxy.ReleaseResponse, error) {
	err := terminal.Restore(int(tp.stdin.Fd()), tp.origState)
	tp.mux.Unlock()
	return &ttyproxy.ReleaseResponse{}, err
}

func (tp *ttyProvider) Resize(_ *ttyproxy.ResizeRequest, stream ttyproxy.TTYProxy_ResizeServer) error {
	g := errgroup.Group{}

	// Handle terminal resize.
	ch := make(chan os.Signal, 1)
	defer close(ch)
	signal.Notify(ch, syscall.SIGWINCH)

	g.Go(func() error {
		for range ch {
			ws, err := unix.IoctlGetWinsize(int(tp.stdin.Fd()), unix.TIOCGWINSZ)
			if err != nil {
				return errors.WithStack(err)
			}

			err = stream.Send(&ttyproxy.ResizeResponse{
				Rows:    uint32(ws.Row),
				Columns: uint32(ws.Col),
				X:       uint32(ws.Xpixel),
				Y:       uint32(ws.Ypixel),
			})
			if err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
	ch <- syscall.SIGWINCH // Initial resize.
	return g.Wait()
}

func (tp *ttyProvider) Proxy(stream ttyproxy.TTYProxy_ProxyServer) error {
	g := errgroup.Group{}

	g.Go(func() error {
		for {
			out, err := stream.Recv()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(out.Data)
			if err != nil {
				return err
			}
		}
	})

	g.Go(func() error {
		dest := &stdinWriter{stream}
		// TODO do we need to interrupt this?
		_, err := io.Copy(dest, tp.stdin)
		return err
	})

	return g.Wait()
}

type stdinWriter struct {
	stream ttyproxy.TTYProxy_ProxyServer
}

func (w *stdinWriter) Write(msg []byte) (int, error) {
	err := w.stream.Send(&ttyproxy.BytesMessage{
		Data: msg,
	})
	return len(msg), err
}
