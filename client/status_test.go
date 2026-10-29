package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/defaults"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSolveStatusMarshal(t *testing.T) {
	now := time.Now().UTC()
	vertex := digest.FromString("vertex")
	tests := []struct {
		name   string
		status *SolveStatus
	}{
		{
			name:   "empty",
			status: &SolveStatus{},
		},
		{
			name: "empty log",
			status: &SolveStatus{Logs: []*VertexLog{
				{Vertex: vertex, Timestamp: now, Stream: 1},
			}},
		},
		{
			name: "all fields",
			status: &SolveStatus{
				Vertexes: []*Vertex{{
					Digest:        vertex,
					Inputs:        []digest.Digest{vertex},
					Name:          "step",
					Started:       &now,
					Completed:     &now,
					Cached:        true,
					Error:         "error",
					ProgressGroup: &pb.ProgressGroup{Id: "group", Name: "group"},
				}},
				Statuses: []*VertexStatus{{Vertex: vertex, ID: "progress", Current: 1, Total: 2, Timestamp: now}},
				Logs:     []*VertexLog{{Vertex: vertex, Data: []byte("output"), Stream: 1, Timestamp: now}},
				Warnings: []*VertexWarning{{Vertex: vertex, Short: []byte("warning")}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			responses := tc.status.Marshal()
			require.Len(t, responses, 1)
			require.Equal(t, tc.status, NewSolveStatus(responses[0]))
		})
	}
}

func TestSolveStatusMarshalBatches(t *testing.T) {
	ss := &SolveStatus{}
	// Each field alone exceeds the batch target, while each item fits a batch.
	for i := range 8 {
		ss.Vertexes = append(ss.Vertexes, &Vertex{Inputs: []digest.Digest{}, Name: strconv.Itoa(i) + strings.Repeat("v", 256*1024)})
		ss.Statuses = append(ss.Statuses, &VertexStatus{Name: strconv.Itoa(i) + strings.Repeat("s", 256*1024)})
		ss.Logs = append(ss.Logs, &VertexLog{Data: bytes.Repeat([]byte{byte(i)}, 256*1024)})
		ss.Warnings = append(ss.Warnings, &VertexWarning{
			Short:      []byte("warning"),
			Detail:     [][]byte{[]byte("detail")},
			Level:      1,
			URL:        "https://example.com/warning",
			SourceInfo: &pb.SourceInfo{Filename: "Dockerfile", Data: bytes.Repeat([]byte{byte(i)}, 256*1024)},
			Range:      []*pb.Range{{Start: &pb.Position{Line: 1}, End: &pb.Position{Line: 2}}},
		})
	}
	responses := ss.Marshal()
	merged := &SolveStatus{}
	for _, response := range responses {
		data, err := proto.Marshal(response)
		require.NoError(t, err)
		require.LessOrEqual(t, len(data), statusResponseSize)
		part := NewSolveStatus(response)
		merged.Vertexes = append(merged.Vertexes, part.Vertexes...)
		merged.Statuses = append(merged.Statuses, part.Statuses...)
		merged.Logs = append(merged.Logs, part.Logs...)
		merged.Warnings = append(merged.Warnings, part.Warnings...)
	}
	// Check against the original counts too: Marshal must not consume the input.
	require.Len(t, ss.Vertexes, 8)
	require.Len(t, ss.Statuses, 8)
	require.Len(t, ss.Logs, 8)
	require.Len(t, ss.Warnings, 8)
	require.Equal(t, ss, merged)
	// A batch should fit three of these entries. A stale size counter would
	// instead emit one response per entry after the first batch.
	require.Len(t, responses, 11)
}

func TestSolveStatusMarshalLargeLog(t *testing.T) {
	for _, size := range []int{statusResponseSize - 128, statusResponseSize, 20 * 1024 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			log := &VertexLog{
				Vertex:    digest.FromString("vertex"),
				Stream:    2,
				Timestamp: time.Now().UTC(),
				Data:      bytes.Repeat([]byte("x"), size),
			}
			ss := &SolveStatus{Logs: []*VertexLog{log}}
			var data []byte
			for _, response := range ss.Marshal() {
				encoded, err := proto.Marshal(response)
				require.NoError(t, err)
				require.LessOrEqual(t, len(encoded), statusResponseSize)
				for _, part := range NewSolveStatus(response).Logs {
					require.Equal(t, log.Vertex, part.Vertex)
					require.Equal(t, log.Stream, part.Stream)
					require.Equal(t, log.Timestamp, part.Timestamp)
					data = append(data, part.Data...)
				}
			}
			require.Equal(t, log.Data, data)
			require.Len(t, ss.Logs, 1)
		})
	}
}

func TestSolveStatusMarshalLargeMetadata(t *testing.T) {
	ss := &SolveStatus{Vertexes: []*Vertex{
		{Name: "before"},
		{Name: strings.Repeat("v", statusResponseSize+1)},
		{Name: "after"},
	}}
	responses := ss.Marshal()
	require.Len(t, responses, 3)
	for i, response := range responses {
		require.Len(t, response.Vertexes, 1)
		require.Equal(t, ss.Vertexes[i].Name, response.Vertexes[0].Name)
	}
}

func TestSolveStatusMarshalLogBoundary(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	vertex := digest.FromString("vertex")
	data := bytes.Repeat([]byte("x"), statusResponseSize)
	response := &controlapi.StatusResponse{Logs: []*controlapi.VertexLog{{
		Vertex:    string(vertex),
		Stream:    2,
		Timestamp: timestamppb.New(now),
		Msg:       data,
	}}}
	// Measure the wire overhead independently of Marshal's size calculation.
	capacity := statusResponseSize - (proto.Size(response) - len(data))
	for _, delta := range []int{-1, 0, 1} {
		t.Run(strconv.Itoa(delta), func(t *testing.T) {
			ss := &SolveStatus{Logs: []*VertexLog{{
				Vertex:    vertex,
				Stream:    2,
				Timestamp: now,
				Data:      data[:capacity+delta],
			}}}
			responses := ss.Marshal()
			if delta <= 0 {
				require.Len(t, responses, 1)
				require.Equal(t, statusResponseSize+delta, proto.Size(responses[0]))
			} else {
				require.Len(t, responses, 2)
				require.Equal(t, statusResponseSize, proto.Size(responses[0]))
				require.Equal(t, []byte("x"), responses[1].Logs[0].Msg)
				ss.Logs = append(ss.Logs, &VertexLog{Data: []byte("tail")})
				ss.Warnings = []*VertexWarning{{Short: []byte("warning")}}
				responses = ss.Marshal()
				require.Len(t, responses, 2)
				require.Len(t, responses[1].Logs, 2)
				require.Equal(t, []byte("tail"), responses[1].Logs[1].Msg)
				require.Len(t, responses[1].Warnings, 1)
			}
		})
	}
}

func TestSolveStatusMarshalOversizedLogMetadata(t *testing.T) {
	// NewSolveStatus can read an arbitrary digest string from a stored record.
	// Preserve it like other indivisible metadata instead of panicking or looping.
	original := &controlapi.StatusResponse{Logs: []*controlapi.VertexLog{{
		Vertex: strings.Repeat("x", statusResponseSize),
		Msg:    []byte("log"),
	}}}
	responses := NewSolveStatus(original).Marshal()
	require.Len(t, responses, 1)
	require.Len(t, responses[0].Logs, 1)
	require.Equal(t, original.Logs[0].Vertex, responses[0].Logs[0].Vertex)
	require.Equal(t, original.Logs[0].Msg, responses[0].Logs[0].Msg)
}

type statusReplayServer struct {
	controlapi.UnimplementedControlServer
	status *SolveStatus
}

func (s *statusReplayServer) Status(_ *controlapi.StatusRequest, stream controlapi.Control_StatusServer) error {
	for _, response := range s.status.Marshal() {
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	return nil
}

func TestSolveStatusMarshalGRPC(t *testing.T) {
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	server := grpc.NewServer(grpc.MaxSendMsgSize(defaults.DefaultMaxSendMsgSize))
	data := bytes.Repeat([]byte("x"), 20*1024*1024)
	controlapi.RegisterControlServer(server, &statusReplayServer{
		status: &SolveStatus{Logs: []*VertexLog{{Data: data}}},
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-serveErr; !errors.Is(err, grpc.ErrServerStopped) {
			require.NoError(t, err)
		}
	})
	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(defaults.DefaultMaxRecvMsgSize)))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	ctx, cancel := context.WithTimeoutCause(t.Context(), 30*time.Second, errors.New("status replay timed out"))
	defer cancel()
	stream, err := controlapi.NewControlClient(conn).Status(ctx, &controlapi.StatusRequest{Ref: "oversized"})
	require.NoError(t, err)
	var received []byte
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		for _, log := range response.Logs {
			received = append(received, log.Msg...)
		}
	}
	require.Equal(t, data, received)
}
