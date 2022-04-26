package sessionio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/stack"
	"github.com/moby/sys/signal"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

// New returns the client which fowards IO with the server.
func New(conn *grpc.ClientConn) (ioClient *IOClient, stdin io.ReadCloser, stdout, stderr io.WriteCloser, err error) {
	c := NewIOForwarderClient(conn)
	pr0, pw0 := io.Pipe()
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()
	return &IOClient{client: c, stdin: pw0, stdout: pr1, stderr: pr2}, pr0, pw1, pw2, nil
}

// NewFromSession returns the client which fowards IO with the server over the specified session.
func NewFromSession(ctx context.Context, sm *session.Manager, sid string) (ioClient *IOClient, stdin io.ReadCloser, stdout, stderr io.WriteCloser, err error) {
	c, err := sm.Get(ctx, sid, false)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return New(c.Conn())
}

// IOClient is the client side of IO forwarding. This gives access to the received stdin data, signal and resize events.
type IOClient struct {
	// SignalFn is a callback function called when a signal is reached to the client.
	SignalFn func(context.Context, syscall.Signal) error

	// ResizeFn is a callback function called when a resize event is reached to the client.
	ResizeFn func(context.Context, WinSize) error

	client    IOForwarderClient
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	done      chan struct{}
	doneOnce  sync.Once
	startOnce sync.Once
	startErr  error
}

// Close finishes forwarding IO and discards resources.
func (s *IOClient) Close() (err error) {
	if cErr := s.stdin.Close(); cErr != nil {
		err = cErr
	}
	if cErr := s.stdout.Close(); cErr != nil {
		err = cErr
	}
	if cErr := s.stderr.Close(); cErr != nil {
		err = cErr
	}
	if s.done != nil {
		s.doneOnce.Do(func() {
			close(s.done)
		})
	}
	return err
}

// Start starts forwarding IO.
func (s *IOClient) Start(ctx context.Context) (retErr error) {
	defer func() {
		if retErr != nil {
			s.startErr = retErr
		}
	}()
	if s.startErr != nil {
		return s.startErr
	}
	s.startOnce.Do(func() {
		retErr = func() (retErr error) {
			stream, err := s.client.ForwardIO(ctx)
			if err != nil {
				return err
			}
			defer func() {
				if retErr != nil {
					stream.CloseSend()
				}
			}()
			if msg, err := stream.Recv(); err != nil {
				return fmt.Errorf("failed to get init message: %w", err)
			} else if initDone := msg.GetInitDone(); initDone == nil {
				return fmt.Errorf("initDone message must be passed at first: %T", msg.GetInput())
			} else if errStr := initDone.Error; errStr != "" {
				return fmt.Errorf("server initialization failed: %v", errStr)
			}
			go func() {
				s.forward(ctx, stream)
				stream.CloseSend()
			}()
			return nil
		}()
	})
	return
}

func (s *IOClient) forward(ctx context.Context, stream IOForwarder_ForwardIOClient) (err error) {
	eg, ctx := errgroup.WithContext(ctx)
	s.done = make(chan struct{})

	var stdoutReader *io.PipeReader
	var stderrReader *io.PipeReader
	eg.Go(func() error {
		<-s.done
		var err error
		if stdoutReader != nil {
			err = stdoutReader.Close()
		}
		if stderrReader != nil {
			err = stderrReader.Close()
		}
		return err
	})

	if s.stdout != nil {
		var stdoutWriter io.WriteCloser
		stdoutReader, stdoutWriter = io.Pipe()
		eg.Go(func() error {
			io.Copy(stdoutWriter, s.stdout)
			stdoutWriter.Close()
			return nil
		})

		eg.Go(func() error {
			m := &msgWriter{
				snd: stream,
				fd:  1,
			}
			err := m.copyFromReader(stdoutReader)
			// ignore ErrClosedPipe, it is EOF for our usage.
			if err != nil && !errors.Is(err, io.ErrClosedPipe) {
				return err
			}
			// not an error so must be eof
			return stream.Send(&Message{
				Input: &Message_File{
					File: &FdMessage{
						Fd:  1,
						EOF: true,
					},
				},
			})
		})
	}

	if s.stderr != nil {
		var stderrWriter io.WriteCloser
		stderrReader, stderrWriter = io.Pipe()
		eg.Go(func() error {
			io.Copy(stderrWriter, s.stderr)
			stderrWriter.Close()
			return nil
		})

		eg.Go(func() error {
			m := &msgWriter{
				snd: stream,
				fd:  2,
			}
			err := m.copyFromReader(stderrReader)
			// ignore ErrClosedPipe, it is EOF for our usage.
			if err != nil && !errors.Is(err, io.ErrClosedPipe) {
				return err
			}
			// not an error so must be eof
			return stream.Send(&Message{
				Input: &Message_File{
					File: &FdMessage{
						Fd:  2,
						EOF: true,
					},
				},
			})
		})
	}

	msgCh := make(chan *Message)
	eg.Go(func() error {
		defer close(msgCh)
		for {
			msg, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return stack.Enable(err)
			}
			select {
			case msgCh <- msg:
			case <-s.done:
				return nil
			case <-ctx.Done():
				return nil
			}
		}
	})
	eg.Go(func() error {
		defer s.Close()
		for {
			var msg *Message
			select {
			case msg = <-msgCh:
			case <-ctx.Done():
				return nil
			}
			if msg == nil {
				return nil
			}
			if file := msg.GetFile(); file != nil {
				if file.Fd != 0 {
					return fmt.Errorf("unexpected fd: %v", file.Fd)
				}
				if len(file.Data) > 0 {
					_, err := s.stdin.Write(file.Data)
					if err != nil {
						return err
					}
				}
				if file.EOF {
					s.stdin.Close()
				}
			} else if resize := msg.GetResize(); resize != nil {
				if s.ResizeFn != nil {
					s.ResizeFn(ctx, WinSize{
						Cols: resize.Cols,
						Rows: resize.Rows,
					})
				}
			} else if sig := msg.GetSignal(); sig != nil {
				if s.SignalFn != nil {
					syscallSignal, ok := signal.SignalMap[sig.Name]
					if !ok {
						continue
					}
					s.SignalFn(ctx, syscallSignal)
				}
			} else {
				return fmt.Errorf("unexpected message: %T", msg.GetInput())
			}
		}
	})

	return stack.Enable(eg.Wait())
}

// NewServerFromSession returns the server side of IO stream. This provides IOs and signal events forwarded from
// the specified session.
func NewServerFromSession(sCtx context.Context, sm *session.Manager, sid string) (*IOServer, error) {
	return NewIOServer(func(ctx context.Context) (IOConfig, error) {
		c, err := sm.Get(ctx, sid, false)
		if err != nil {
			return IOConfig{}, err
		}
		ioClient, stdin, stdout, stderr, err := New(c.Conn())
		if err != nil {
			return IOConfig{}, err
		}
		signal := make(chan syscall.Signal, 1)
		resize := make(chan WinSize, 1)
		ioClient.SignalFn = func(ctx context.Context, sig syscall.Signal) error {
			signal <- sig
			return nil
		}
		ioClient.ResizeFn = func(ctx context.Context, win WinSize) error {
			resize <- win
			return nil
		}
		if err := ioClient.Start(ctx); err != nil {
			ioClient.Close()
			return IOConfig{}, err
		}
		return IOConfig{Stdin: stdin, Stdout: stdout, Stderr: stderr,
			Signal: signal, Resize: resize, Close: ioClient.Close}, nil
	}), nil
}

// InitIOFn is a callback provides IO configuration which will be forwarded to/from the client.
// This will be called each time IOServer starts IO stream with a client.
// TODO: InitIOFn should receive information about the caller to determine if allowing IO
type InitIOFn func(ctx context.Context) (IOConfig, error)

// IOConfig is configuration of IO which will be forwarded from server to/from the client.
type IOConfig struct {
	Stdin          io.ReadCloser
	Stdout, Stderr io.WriteCloser
	Signal         <-chan syscall.Signal
	Resize         <-chan WinSize
	Close          func() error
}

// NewIOServer is the server side of IO stream. Each time ForwardIO API is called, this calls InitIOFn
// and forwards the provided IO to/from the client.
func NewIOServer(initIOFn InitIOFn) *IOServer {
	return &IOServer{initIOFn: initIOFn}
}

type IOServer struct {
	initIOFn InitIOFn
}

type WinSize struct {
	Rows uint32
	Cols uint32
}

// ForwardIO starts forwarding IO over the provided stream.
func (s *IOServer) ForwardIO(srv IOForwarder_ForwardIOServer) error {
	ctx := srv.Context()
	initStart := time.Now()

	stream := &debugStream{srv, "server=" + initStart.String()}
	cfg, err := s.initIOFn(ctx)
	if err != nil {
		if dErr := stream.Send(&Message{
			Input: &Message_InitDone{
				InitDone: &InitDoneMessage{
					Error: fmt.Sprintf("failed to init: %v", err),
				},
			},
		}); dErr != nil {
			err = fmt.Errorf("%v, %w", dErr, err)
		}
		return err
	}
	defer func() {
		if err := cfg.Close(); err != nil {
			bklog.G(ctx).WithError(err).WithField("initialized", initStart).Warnf("failed to finalize")
		}
	}()
	if err := stream.Send(&Message{
		Input: &Message_InitDone{
			InitDone: &InitDoneMessage{},
		},
	}); err != nil {
		return err
	}
	eg, ctx := errgroup.WithContext(ctx)
	done := make(chan struct{})

	var stdinReader *io.PipeReader
	eg.Go(func() error {
		<-done
		if stdinReader != nil {
			return stdinReader.Close()
		}
		return nil
	})

	if cfg.Stdin != nil {
		var stdinWriter io.WriteCloser
		stdinReader, stdinWriter = io.Pipe()
		eg.Go(func() error {
			io.Copy(stdinWriter, cfg.Stdin)
			stdinWriter.Close()
			return nil
		})

		eg.Go(func() error {
			m := &msgWriter{
				snd: stream,
				fd:  0,
			}
			err := m.copyFromReader(stdinReader)
			// ignore ErrClosedPipe, it is EOF for our usage.
			if err != nil && !errors.Is(err, io.ErrClosedPipe) {
				return err
			}
			// not an error so must be eof
			return stream.Send(&Message{
				Input: &Message_File{
					File: &FdMessage{
						Fd:  0,
						EOF: true,
					},
				},
			})
		})
	}

	eg.Go(func() error {
		for {
			var sig syscall.Signal
			select {
			case sig = <-cfg.Signal:
			case <-done:
				return nil
			case <-ctx.Done():
				return nil
			}
			name := sigToName[sig]
			if name == "" {
				continue
			}
			if err := stream.Send(&Message{
				Input: &Message_Signal{
					Signal: &SignalMessage{
						Name: name,
					},
				},
			}); err != nil {
				return fmt.Errorf("failed to send signal: %w", err)
			}
		}
	})

	eg.Go(func() error {
		for {
			var win WinSize
			select {
			case win = <-cfg.Resize:
			case <-done:
				return nil
			case <-ctx.Done():
				return nil
			}
			if err := stream.Send(&Message{
				Input: &Message_Resize{
					Resize: &ResizeMessage{
						Rows: win.Rows,
						Cols: win.Cols,
					},
				},
			}); err != nil {
				return fmt.Errorf("failed to send resize: %w", err)
			}
		}
	})

	msgCh := make(chan *Message)
	eg.Go(func() error {
		defer close(msgCh)
		for {
			msg, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return stack.Enable(err)
			}
			select {
			case msgCh <- msg:
			case <-done:
				return nil
			case <-ctx.Done():
				return nil
			}
		}
	})

	eg.Go(func() error {
		eofs := make(map[uint32]struct{})
		defer close(done)
		for {
			var msg *Message
			select {
			case msg = <-msgCh:
			case <-ctx.Done():
				return nil
			}
			if msg == nil {
				return nil
			}
			if file := msg.GetFile(); file != nil {
				if _, ok := eofs[file.Fd]; ok {
					continue
				}
				var out io.WriteCloser
				switch file.Fd {
				case 1:
					out = cfg.Stdout
				case 2:
					out = cfg.Stderr
				}
				if out == nil {
					// if things are plumbed correctly this should never happen
					return fmt.Errorf("missing writer for output fd %d", file.Fd)
				}
				if len(file.Data) > 0 {
					_, err := out.Write(file.Data)
					if err != nil {
						return err
					}
				}
				if file.EOF {
					eofs[file.Fd] = struct{}{}
				}
			} else {
				return fmt.Errorf("unexpected message: %T", msg.GetInput())
			}
		}
	})

	return stack.Enable(eg.Wait())
}

var sigToName = map[syscall.Signal]string{}

func init() {
	for name, value := range signal.SignalMap {
		sigToName[value] = name
	}
}

type sender interface {
	Send(*Message) error
}

type msgWriter struct {
	snd sender
	fd  uint32
}

func (w *msgWriter) copyFromReader(r io.Reader) error {
	for {
		buf := make([]byte, 32*1024)
		n, err := r.Read(buf)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		} else if n > 0 {
			if w.snd.Send(&Message{
				Input: &Message_File{
					File: &FdMessage{
						Fd:   w.fd,
						Data: buf[:n],
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

type msgStream interface {
	Send(*Message) error
	Recv() (*Message, error)
}

type debugStream struct {
	msgStream
	prefix string
}

func (s *debugStream) Send(msg *Message) error {
	ctx := context.TODO()
	switch m := msg.GetInput().(type) {
	case *Message_InitDone:
		bklog.G(ctx).Debugf("|---> InitDone Message (sender:%v)", s.prefix)
	case *Message_File:
		if m.File.EOF {
			bklog.G(ctx).Debugf("|---> File Message (sender:%v) fd=%d, EOF", s.prefix, m.File.Fd)
		} else {
			bklog.G(ctx).Debugf("|---> File Message (sender:%v) fd=%d, %d bytes", s.prefix, m.File.Fd, len(m.File.Data))
		}
	case *Message_Resize:
		bklog.G(ctx).Debugf("|---> Resize Message (sender:%v): %+v", s.prefix, m.Resize)
	case *Message_Signal:
		bklog.G(ctx).Debugf("|---> Signal Message (sender:%v): %s", s.prefix, m.Signal.Name)
	}
	return s.msgStream.Send(msg)
}

func (s *debugStream) Recv() (*Message, error) {
	ctx := context.TODO()
	msg, err := s.msgStream.Recv()
	if err != nil {
		return nil, err
	}
	switch m := msg.GetInput().(type) {
	case *Message_InitDone:
		bklog.G(ctx).Debugf("|<--- InitDone Message (receiver:%v)", s.prefix)
	case *Message_File:
		if m.File.EOF {
			bklog.G(ctx).Debugf("|<--- File Message (receiver:%v) fd=%d, EOF", s.prefix, m.File.Fd)
		} else {
			bklog.G(ctx).Debugf("|<--- File Message (receiver:%v) fd=%d, %d bytes", s.prefix, m.File.Fd, len(m.File.Data))
		}
	case *Message_Resize:
		bklog.G(ctx).Debugf("|<--- Resize Message (receiver:%v): %+v", s.prefix, m.Resize)
	case *Message_Signal:
		bklog.G(ctx).Debugf("|<--- Signal Message (receiver:%v): %s", s.prefix, m.Signal.Name)
	}
	return msg, nil
}
