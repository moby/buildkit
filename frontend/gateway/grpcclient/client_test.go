package grpcclient

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	pb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// blockedSendStream is an ExecProcess stream whose Send waits for flow-control
// credit that never arrives until release is closed, while Recv keeps
// delivering server messages.
type blockedSendStream struct {
	pb.LLBBridge_ExecProcessClient
	sending  chan struct{}
	release  chan struct{}
	received chan *pb.ExecMessage
}

func (s *blockedSendStream) Send(*pb.ExecMessage) error {
	s.sending <- struct{}{}
	<-s.release
	return nil
}

func (s *blockedSendStream) Recv() (*pb.ExecMessage, error) {
	msg, ok := <-s.received
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

type blockedSendClient struct {
	pb.LLBBridgeClient
	stream *blockedSendStream
}

func (c blockedSendClient) ExecProcess(context.Context, ...grpc.CallOption) (pb.LLBBridge_ExecProcessClient, error) {
	return c.stream, nil
}

// A process whose stdin send is blocked on flow control must keep receiving
// its output: the server only resumes reading stdin once the process consumes
// it, and the process only continues once its output is drained.
func TestMessageForwarderSendDoesNotBlockReceive(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(t.Context(), 10*time.Second, errors.New("message forwarding test timed out"))
	defer cancel()
	stream := &blockedSendStream{
		sending:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		received: make(chan *pb.ExecMessage, 1),
	}
	forwarder := newMessageForwarder(t.Context(), blockedSendClient{stream: stream})
	require.NoError(t, forwarder.Start())
	process := forwarder.Register("process")

	sent := make(chan error, 1)
	t.Cleanup(func() {
		close(stream.release)
		close(stream.received)
		require.NoError(t, forwarder.Release())
		require.NoError(t, <-sent)
	})
	go func() {
		sent <- forwarder.Send(&pb.ExecMessage{
			ProcessID: "process",
			Input:     &pb.ExecMessage_File{File: &pb.FdMessage{Fd: 0, Data: []byte("stdin")}},
		})
	}()
	select {
	case <-stream.sending:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}

	stream.received <- &pb.ExecMessage{
		ProcessID: "process",
		Input:     &pb.ExecMessage_File{File: &pb.FdMessage{Fd: 1, Data: []byte("stdout")}},
	}
	msg, ok := process.Recv(ctx)
	require.True(t, ok)
	require.NotNil(t, msg, "process output stalled behind a blocked stdin send")
	require.Equal(t, "stdout", string(msg.GetFile().GetData()))
}

func TestMessageForwarderSerializesSends(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(t.Context(), 10*time.Second, errors.New("stream send timed out"))
	defer cancel()
	stream := &blockedSendStream{
		sending: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	forwarder := newMessageForwarder(ctx, nil)
	forwarder.stream = stream
	forwarder.Register("process")

	release := sync.OnceFunc(func() { close(stream.release) })
	sent := make(chan error, 2)
	t.Cleanup(func() {
		release()
		require.NoError(t, forwarder.Release())
		for range 2 {
			require.NoError(t, <-sent)
		}
	})
	for range 2 {
		go func() {
			sent <- forwarder.Send(&pb.ExecMessage{ProcessID: "process"})
		}()
	}
	select {
	case <-stream.sending:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	select {
	case <-stream.sending:
		t.Fatal("concurrent sends entered the stream")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case <-stream.sending:
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
}
