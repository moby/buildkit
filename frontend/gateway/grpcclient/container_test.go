package grpcclient

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/moby/buildkit/frontend/gateway/client"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type outputErrorClient struct {
	pb.LLBBridgeClient
	pb.LLBBridge_ExecProcessClient
	ctx      context.Context
	messages chan *pb.ExecMessage
}

func (c *outputErrorClient) ExecProcess(ctx context.Context, _ ...grpc.CallOption) (pb.LLBBridge_ExecProcessClient, error) {
	c.ctx = ctx
	return c, nil
}

func (c *outputErrorClient) Send(msg *pb.ExecMessage) error {
	if msg.GetInit() != nil {
		c.messages <- &pb.ExecMessage{
			ProcessID: msg.ProcessID,
			Input:     &pb.ExecMessage_Started{Started: &pb.StartedMessage{}},
		}
	}
	return nil
}

func (c *outputErrorClient) Recv() (*pb.ExecMessage, error) {
	select {
	case msg := <-c.messages:
		return msg, nil
	case <-c.ctx.Done():
		return nil, io.EOF
	}
}

func TestProcessWaitReturnsOutputError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fd    uint32
		stdin bool
	}{
		{name: "stdout", fd: 1},
		{name: "stderr", fd: 2},
		{name: "stdout-with-stdin", fd: 1, stdin: true},
		{name: "stderr-with-stdin", fd: 2, stdin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeoutCause(t.Context(), 5*time.Second, errors.New("process output-error test timed out"))
			defer cancel()
			bridge := &outputErrorClient{messages: make(chan *pb.ExecMessage, 1)}
			forwarder := newMessageForwarder(ctx, bridge)
			require.NoError(t, forwarder.Start())
			t.Cleanup(func() { require.NoError(t, forwarder.Release()) })
			ctr := &container{id: "output-error", execMsgs: forwarder}

			outputReader, outputWriter := io.Pipe()
			require.NoError(t, outputReader.Close())
			t.Cleanup(func() { outputWriter.Close() })
			req := client.StartRequest{}
			if tc.fd == 1 {
				req.Stdout = outputWriter
			} else {
				req.Stderr = outputWriter
			}
			if tc.stdin {
				inputReader, inputWriter := io.Pipe()
				t.Cleanup(func() {
					inputReader.Close()
					inputWriter.Close()
				})
				req.Stdin = inputReader
			}
			process, err := ctr.Start(ctx, req)
			require.NoError(t, err)
			bridge.messages <- &pb.ExecMessage{
				ProcessID: process.(*containerProcess).id,
				Input:     &pb.ExecMessage_File{File: &pb.FdMessage{Fd: tc.fd, Data: []byte("output")}},
			}
			wait := make(chan error, 1)
			go func() { wait <- process.Wait() }()
			select {
			case err := <-wait:
				require.ErrorIs(t, err, io.ErrClosedPipe)
			case <-ctx.Done():
				t.Fatal(context.Cause(ctx))
			}
		})
	}
}
