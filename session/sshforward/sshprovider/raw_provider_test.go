package sshprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/moby/buildkit/session/sshforward"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// echoServer implements a message based echo server
// It expects to send and receive discreete json messages over a net.Conn.
// Using discreete messages is helpful for testing purposes over a literal echo.
type echoServer struct{}

type echoRequest struct {
	Data string `json:"data"`
}

type echoResponse struct {
	Recvd string `json:"recvd"`
	Count int    `json:"count"`
}

func (es *echoServer) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func() {
			dec := json.NewDecoder(conn)
			enc := json.NewEncoder(conn)

			var req echoRequest
			var resp echoResponse

			for i := 1; ; i++ {
				if err := dec.Decode(&req); err != nil {
					conn.Close()
					return
				}

				resp.Recvd = req.Data
				resp.Count = i
				if err := enc.Encode(&resp); err != nil {
					conn.Close()
					return
				}
			}
		}()
	}
}

func dialerFnToGRPCDialer(dialer func(ctx context.Context) (net.Conn, error)) grpc.DialOption {
	return grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		return dialer(ctx)
	})
}

func TestRawProvider(t *testing.T) {
	handlerListener := &pipeListener{}
	defer handlerListener.Close()

	echoListener := &pipeListener{}
	defer echoListener.Close()
	echo := &echoServer{}
	go echo.Serve(echoListener)

	handler := &socketProvider{
		m: map[string]dialerFn{
			"test": echoListener.Dialer,
		},
	}

	srv := grpc.NewServer()
	handler.Register(srv)

	// Start proxy handler service
	go srv.Serve(handlerListener)

	// passthrough:// is a special scheme that allows us to use the handlerListener's Dialer directly
	// otherwise grpc will try to resolve whatever we put in there.
	c, err := grpc.NewClient("passthrough://", dialerFnToGRPCDialer(handlerListener.Dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer c.Close()

	client := sshforward.NewSSHClient(c)

	ctx := context.Background()

	_, err = client.CheckAgent(ctx, &sshforward.CheckAgentRequest{ID: "does-not-exist"})
	require.ErrorContains(t, err, "does-not-exis")

	_, err = client.CheckAgent(ctx, &sshforward.CheckAgentRequest{ID: "test"})
	require.NoError(t, err)

	ctx = metadata.AppendToOutgoingContext(ctx, sshforward.KeySSHID, "test")
	stream, err := client.ForwardAgent(ctx)
	require.NoError(t, err)

	defer stream.CloseSend()

	req := echoRequest{Data: "hello, world!"}
	sw := &streamWriter{stream: stream}

	enc := json.NewEncoder(sw)
	err = enc.Encode(&req)
	require.NoError(t, err)

	sr := &streamReader{stream: stream}
	var resp echoResponse
	dec := json.NewDecoder(sr)

	err = dec.Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, req.Data, resp.Recvd)
	assert.Equal(t, 1, resp.Count)

	req.Data = "another message"
	err = enc.Encode(&req)
	require.NoError(t, err)

	err = dec.Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, resp.Recvd, req.Data)
	assert.Equal(t, 2, resp.Count)

	// Test larger message
	req.Data = strings.Repeat("x", 10000)
	err = enc.Encode(&req)
	require.NoError(t, err)

	err = dec.Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, resp.Recvd, req.Data)
	assert.Equal(t, 3, resp.Count)
}

type streamWriter struct {
	stream grpc.BidiStreamingClient[sshforward.BytesMessage, sshforward.BytesMessage]
}

func (sw *streamWriter) Write(p []byte) (n int, err error) {
	msg := &sshforward.BytesMessage{Data: p}
	if err := sw.stream.Send(msg); err != nil {
		return 0, err
	}

	return len(p), nil
}

type streamReader struct {
	stream grpc.BidiStreamingClient[sshforward.BytesMessage, sshforward.BytesMessage]
	buf    []byte
}

func (sr *streamReader) Read(p []byte) (int, error) {
	if len(sr.buf) > 0 {
		n := copy(p, sr.buf)
		sr.buf = sr.buf[n:]
		return n, nil
	}

	msg, err := sr.stream.Recv()
	if err != nil {
		return 0, err
	}

	n := copy(p, msg.Data)
	if n < len(msg.Data) {
		sr.buf = append(sr.buf, msg.Data[n:]...)
	}
	return n, nil
}
