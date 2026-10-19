package grpcclient

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/moby/buildkit/frontend/gateway/client"
	pb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type execStreamServer struct {
	pb.UnimplementedLLBBridgeServer
	init    chan string
	failure chan error
	start   bool
	finish  bool
}

func (s *execStreamServer) ExecProcess(stream pb.LLBBridge_ExecProcessServer) error {
	var pids []string
	received := make(chan *pb.ExecMessage)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case received <- msg:
			case <-stream.Context().Done():
				return
			}
		}
	}()
	for {
		select {
		case err := <-s.failure:
			if s.finish {
				for _, pid := range pids {
					for _, msg := range []*pb.ExecMessage{
						{ProcessID: pid, Input: &pb.ExecMessage_Exit{Exit: &pb.ExitMessage{}}},
						{ProcessID: pid, Input: &pb.ExecMessage_Done{Done: &pb.DoneMessage{}}},
					} {
						if err := stream.Send(msg); err != nil {
							return err
						}
					}
				}
			}
			return err
		case <-stream.Context().Done():
			return stream.Context().Err()
		case msg := <-received:
			if msg.GetInit() != nil {
				pids = append(pids, msg.ProcessID)
				s.init <- msg.ProcessID
				if s.start {
					if err := stream.Send(&pb.ExecMessage{
						ProcessID: msg.ProcessID,
						Input:     &pb.ExecMessage_Started{Started: &pb.StartedMessage{}},
					}); err != nil {
						return err
					}
				}
			}
		}
	}
}

func TestExecStreamFailureReleasesProcesses(t *testing.T) {
	for _, transport := range []struct {
		name string
		err  error
	}{
		{name: "EOF"},
		{name: "unavailable", err: status.Error(codes.Unavailable, "connection lost")},
		{name: "canceled", err: status.Error(codes.Canceled, "remote session ended")},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "remote exec deadline exceeded")},
	} {
		for _, phase := range []string{"starting", "waiting", "completed"} {
			t.Run(transport.name+"/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithTimeoutCause(t.Context(), 10*time.Second, errors.New("exec stream failure test timed out"))
				defer cancel()
				server := &execStreamServer{
					init:    make(chan string, 2),
					failure: make(chan error, 1),
					start:   phase != "starting",
					finish:  phase == "completed",
				}
				var lc net.ListenConfig
				listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { listener.Close() })
				srv := grpc.NewServer()
				pb.RegisterLLBBridgeServer(srv, server)
				t.Cleanup(srv.Stop)
				go srv.Serve(listener)
				conn, err := grpc.NewClient(listener.Addr().String(),
					grpc.WithTransportCredentials(insecure.NewCredentials()),
				)
				require.NoError(t, err)
				t.Cleanup(func() { conn.Close() })
				forwarder := newMessageForwarder(ctx, pb.NewLLBBridgeClient(conn))
				require.NoError(t, forwarder.Start())
				t.Cleanup(func() {
					err := forwarder.Release()
					if code := status.Code(transport.err); transport.err == nil || code == codes.Canceled {
						require.NoError(t, err)
					} else {
						require.Equal(t, code, status.Code(err))
						require.ErrorContains(t, err, status.Convert(transport.err).Message())
					}
				})
				ctr := &container{id: "stream-failure", execMsgs: forwarder}
				done := make(chan error, 2)
				ready := make(chan struct{}, 2)
				for range 2 {
					req := client.StartRequest{}
					if !server.finish {
						input, writer := io.Pipe()
						t.Cleanup(func() {
							input.Close()
							writer.Close()
						})
						req.Stdin = input
					}
					go func() {
						process, err := ctr.Start(ctx, req)
						if err == nil {
							ready <- struct{}{}
							err = process.Wait()
						}
						done <- err
					}()
					select {
					case <-server.init:
					case <-ctx.Done():
						t.Fatal(context.Cause(ctx))
					}
					if server.start {
						select {
						case <-ready:
						case <-ctx.Done():
							t.Fatal(context.Cause(ctx))
						}
					}
				}
				server.failure <- transport.err
				for range 2 {
					select {
					case err := <-done:
						require.NoError(t, context.Cause(ctx), "caller context must remain live")
						if server.finish {
							require.NoError(t, err)
						} else if transport.err == nil {
							require.ErrorIs(t, err, io.EOF)
						} else {
							require.Equal(t, status.Code(transport.err), status.Code(err))
							require.ErrorContains(t, err, status.Convert(transport.err).Message())
						}
					case <-ctx.Done():
						t.Fatal(context.Cause(ctx))
					}
				}
				// Release cancels the stream itself, so wait for the server's status,
				// which can arrive after completed processes return.
				<-forwarder.ctx.Done()
				require.NoError(t, context.Cause(ctx), "remote exec stream did not end")
			})
		}
	}
}

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
