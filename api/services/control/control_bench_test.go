package moby_buildkit_v1 //nolint:revive,staticcheck

import (
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	proto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Buf is used to prevent the benchmark from being optimized away.
var Buf []byte

func BenchmarkMarshalVertex(b *testing.B) {
	v := sampleVertex()
	for i := 0; i < b.N; i++ {
		var err error
		Buf, err = v.MarshalVT()
		require.NoError(b, err)
	}
}

func BenchmarkMarshalVertexStatus(b *testing.B) {
	v := sampleVertexStatus()
	for i := 0; i < b.N; i++ {
		var err error
		Buf, err = v.MarshalVT()
		require.NoError(b, err)
	}
}

func BenchmarkMarshalVertexLog(b *testing.B) {
	v := sampleVertexLog()
	for i := 0; i < b.N; i++ {
		var err error
		Buf, err = v.MarshalVT()
		require.NoError(b, err)
	}
}

var VertexOutput Vertex

func BenchmarkUnmarshalVertex(b *testing.B) {
	v := sampleVertex()
	buf, err := proto.Marshal(v)
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		err := VertexOutput.UnmarshalVT(buf)
		require.NoError(b, err)
	}
}

var VertexStatusOutput VertexStatus

func BenchmarkUnmarshalVertexStatus(b *testing.B) {
	v := sampleVertexStatus()
	buf, err := proto.Marshal(v)
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		err := VertexStatusOutput.UnmarshalVT(buf)
		require.NoError(b, err)
	}
}

var VertexLogOutput VertexLog

func BenchmarkUnmarshalVertexLog(b *testing.B) {
	v := sampleVertexLog()
	buf, err := proto.Marshal(v)
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		err := VertexLogOutput.UnmarshalVT(buf)
		require.NoError(b, err)
	}
}

func sampleVertex() *Vertex {
	now := time.Now()
	started := now.Add(-time.Minute)
	return &Vertex{
		Digest: string(digest.FromString("abc")),
		Inputs: []string{
			string(digest.FromString("dep1")),
			string(digest.FromString("dep2")),
		},
		Name:      "abc",
		Started:   timestamppb.New(started),
		Completed: timestamppb.New(now),
	}
}

func sampleVertexStatus() *VertexStatus {
	now := time.Now()
	started := now.Add(-time.Minute)
	return &VertexStatus{
		ID:        "abc",
		Vertex:    string(digest.FromString("abc")),
		Name:      "abc",
		Current:   1024,
		Total:     1024,
		Timestamp: timestamppb.New(now),
		Started:   timestamppb.New(started),
		Completed: timestamppb.New(now),
	}
}

func sampleVertexLog() *VertexLog {
	now := time.Now()
	return &VertexLog{
		Vertex:    string(digest.FromString("abc")),
		Timestamp: timestamppb.New(now),
		Stream:    1,
		Msg:       []byte("this is a log message"),
	}
}
