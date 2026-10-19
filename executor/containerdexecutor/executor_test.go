package containerdexecutor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	ctd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	gatewayapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/stretchr/testify/require"
)

func TestContainerdUnknownExitStatus(t *testing.T) {
	// There are assumptions in the containerd executor that the UnknownExitStatus
	// used in errdefs.ExitError matches the variable in the containerd package.
	if ctd.UnknownExitStatus != gatewayapi.UnknownExitStatus {
		t.Fatal("containerd.UnknownExitStatus != errdefs.UnknownExitStatus")
	}
}

func TestStdinForwarding(t *testing.T) {
	copyErr := errors.New("copy failed")
	for _, tc := range []struct {
		name     string
		input    io.Reader
		writeErr error
		want     string
		wantErr  error
	}{
		{
			name:  "empty",
			input: strings.NewReader(""),
		},
		{
			name:  "writer to",
			input: strings.NewReader("testing"),
			want:  "testing",
		},
		{
			name:  "data with EOF",
			input: iotest.DataErrReader(strings.NewReader("testing")),
			want:  "testing",
		},
		{
			name:    "read error",
			input:   iotest.ErrReader(copyErr),
			wantErr: copyErr,
		},
		{
			name:     "write error",
			input:    strings.NewReader("testing"),
			writeErr: copyErr,
			wantErr:  copyErr,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, done := wrapStdin(tc.input)
			out := &stdinTestWriter{t: t, done: done, err: tc.writeErr}
			_, err := io.CopyBuffer(out, r, make([]byte, 32))
			require.ErrorIs(t, err, tc.wantErr)
			require.Equal(t, tc.want, out.buf.String())
			select {
			case <-done:
			default:
				t.Fatal("stdin completion was not reported")
			}
		})
	}
	t.Run("nil", func(t *testing.T) {
		r, done := wrapStdin(nil)
		require.Nil(t, r)
		require.Nil(t, done)
	})
}

type stdinTestWriter struct {
	buf  bytes.Buffer
	t    *testing.T
	done <-chan struct{}
	err  error
}

func (w *stdinTestWriter) Write(p []byte) (int, error) {
	select {
	case <-w.done:
		w.t.Error("stdin completion reported before the write finished")
	default:
	}
	if w.err != nil {
		return 0, w.err
	}
	return w.buf.Write(p)
}

func TestRunProcessStdin(t *testing.T) {
	for _, tc := range []struct {
		name            string
		copyBeforeStart bool
	}{
		{
			name:            "copy before start",
			copyBeforeStart: true,
		},
		{
			name: "copy after start",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newStdinTestProcess()
				stdinDone := make(chan struct{})
				result := make(chan error, 1)
				go func() {
					result <- (&containerdExecutor{}).runProcess(t.Context(), p, stdinDone, nil, nil, nil, nil)
				}()
				if tc.copyBeforeStart {
					close(stdinDone)
				}
				synctest.Wait()
				require.Empty(t, p.stdinClosed, "CloseIO called before Start returned")
				close(p.start)
				if !tc.copyBeforeStart {
					synctest.Wait()
					require.Empty(t, p.stdinClosed, "CloseIO called before input finished")
					close(stdinDone)
				}
				<-p.stdinClosed
				p.status <- *ctd.NewExitStatus(0, time.Now(), nil)
				require.NoError(t, <-result)
			})
		})
	}
}

func TestRunProcessOpenStdin(t *testing.T) {
	for _, cancelProcess := range []bool{false, true} {
		name := "exit"
		if cancelProcess {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(context.Canceled)
				p := newStdinTestProcess()
				close(p.start)
				result := make(chan error, 1)
				go func() {
					result <- (&containerdExecutor{}).runProcess(ctx, p, make(chan struct{}), nil, nil, nil, nil)
				}()
				synctest.Wait()
				if cancelProcess {
					cancel(context.Canceled)
					require.Error(t, <-result)
				} else {
					p.status <- *ctd.NewExitStatus(0, time.Now(), nil)
					require.NoError(t, <-result)
				}
				synctest.Wait()
				require.Empty(t, p.stdinClosed)
			})
		})
	}
}

type stdinTestProcess struct {
	ctd.Process
	start       chan struct{}
	stdinClosed chan struct{}
	status      chan ctd.ExitStatus
}

func newStdinTestProcess() *stdinTestProcess {
	return &stdinTestProcess{
		start:       make(chan struct{}),
		stdinClosed: make(chan struct{}, 1),
		status:      make(chan ctd.ExitStatus, 1),
	}
}

func (p *stdinTestProcess) Start(context.Context) error {
	<-p.start
	return nil
}

func (p *stdinTestProcess) Wait(context.Context) (<-chan ctd.ExitStatus, error) {
	return p.status, nil
}

func (p *stdinTestProcess) CloseIO(_ context.Context, opts ...ctd.IOCloserOpts) error {
	var info ctd.IOCloseInfo
	for _, opt := range opts {
		opt(&info)
	}
	if info.Stdin {
		p.stdinClosed <- struct{}{}
	}
	return nil
}

func (p *stdinTestProcess) Kill(context.Context, syscall.Signal, ...ctd.KillOpts) error {
	p.status <- *ctd.NewExitStatus(137, time.Now(), nil)
	return nil
}

func (p *stdinTestProcess) IO() cio.IO {
	return stdinTestIO{}
}

type stdinTestIO struct {
	cio.IO
}

func (stdinTestIO) Wait() {
}

func (stdinTestIO) Cancel() {
}

func (stdinTestIO) Close() error {
	return nil
}
