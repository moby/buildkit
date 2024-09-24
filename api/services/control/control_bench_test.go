package moby_buildkit_v1 //nolint:revive

import (
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// Buf is used to prevent the benchmark from being optimized away.
var Buf []byte

func BenchmarkMarshalVertex(b *testing.B) {
	v := sampleVertex()
	for i := 0; i < b.N; i++ {
		var err error
		Buf, err = v.Marshal()
		require.NoError(b, err)
	}
}

func BenchmarkMarshalVertexStatus(b *testing.B) {
	v := sampleVertexStatus()
	for i := 0; i < b.N; i++ {
		var err error
		Buf, err = v.Marshal()
		require.NoError(b, err)
	}
}

func BenchmarkMarshalVertexLog(b *testing.B) {
	v := sampleVertexLog()
	for i := 0; i < b.N; i++ {
		var err error
		Buf, err = v.Marshal()
		require.NoError(b, err)
	}
}

var VertexOutput Vertex

func BenchmarkUnmarshalVertex(b *testing.B) {
	v := sampleVertex()
	buf, err := v.Marshal()
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		err := VertexOutput.Unmarshal(buf)
		require.NoError(b, err)
	}
}

var VertexStatusOutput VertexStatus

func BenchmarkUnmarshalVertexStatus(b *testing.B) {
	v := sampleVertexStatus()
	buf, err := v.Marshal()
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		err := VertexStatusOutput.Unmarshal(buf)
		require.NoError(b, err)
	}
}

var VertexLogOutput VertexLog

func BenchmarkUnmarshalVertexLog(b *testing.B) {
	v := sampleVertexLog()
	buf, err := v.Marshal()
	require.NoError(b, err)

	for i := 0; i < b.N; i++ {
		err := VertexLogOutput.Unmarshal(buf)
		require.NoError(b, err)
	}
}

func sampleVertex() *Vertex {
	now := time.Now()
	started := now.Add(-time.Minute)
	return &Vertex{
		Digest: digest.FromString("abc"),
		Inputs: []digest.Digest{
			digest.FromString("dep1"),
			digest.FromString("dep2"),
		},
		Name:      "abc",
		Started:   &started,
		Completed: &now,
	}
}

func sampleVertexStatus() *VertexStatus {
	now := time.Now()
	started := now.Add(-time.Minute)
	return &VertexStatus{
		ID:        "abc",
		Vertex:    digest.FromString("abc"),
		Name:      "abc",
		Current:   1024,
		Total:     1024,
		Timestamp: now,
		Started:   &started,
		Completed: &now,
	}
}

func sampleVertexLog() *VertexLog {
	now := time.Now()
	return &VertexLog{
		Vertex:    digest.FromString("abc"),
		Timestamp: now,
		Stream:    1,
		Msg:       []byte("this is a log message"),
	}
}
