package compression

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestZstdCompressThreads(t *testing.T) {
	// larger than the 16-32MB job size of the parallel encoder so that the
	// input is split into multiple jobs
	src := zstdTestData(40 << 20)

	for _, tc := range []struct {
		name string
		comp Config
	}{
		{name: "default level", comp: New(Zstd)},
		{name: "fastest", comp: New(Zstd).SetLevel(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sequential := zstdCompress(t, tc.comp, src, 0)
			require.True(t, bytes.Equal(src, zstdDecompress(t, sequential)), "sequential output does not roundtrip")

			// a single thread is the sequential encoder
			require.True(t, bytes.Equal(sequential, zstdCompress(t, tc.comp.SetThreads(1), src, 0)), "single-threaded output differs from default")

			parallel := zstdCompress(t, tc.comp.SetThreads(2), src, 0)
			require.True(t, bytes.Equal(src, zstdDecompress(t, parallel)), "parallel output does not roundtrip")
			// jobs are compressed with a limited history, so a different output
			// is the only observable proof that the parallel encoder was used
			require.False(t, bytes.Equal(parallel, sequential), "parallel output is identical to the sequential output")

			// the output must not depend on the number of threads or on how
			// the input is chunked, otherwise layer digests would not be
			// reproducible across machines
			require.True(t, bytes.Equal(parallel, zstdCompress(t, tc.comp.SetThreads(4), src, 0)), "output depends on the number of threads")
			require.True(t, bytes.Equal(parallel, zstdCompress(t, tc.comp.SetThreads(4), src, 4096)), "output depends on the write chunk size")
			if runtime.GOMAXPROCS(0) > 1 {
				require.True(t, bytes.Equal(parallel, zstdCompress(t, tc.comp.SetThreads(0), src, 0)), "output with all CPUs differs")
			}
		})
	}
}

func TestZstdCompressThreadsSmall(t *testing.T) {
	// inputs that fit in a single job or a single block
	for _, size := range []int{0, 1, 1000, 1 << 20} {
		src := zstdTestData(size)
		for _, threads := range []int{0, 1, 4} {
			out := zstdCompress(t, New(Zstd).SetThreads(threads), src, 0)
			require.Equal(t, src, zstdDecompress(t, out), "size=%d threads=%d", size, threads)
		}
	}
}

func BenchmarkZstdCompressThreads(b *testing.B) {
	// several 32MB jobs, otherwise the parallel encoder cannot scale
	src := zstdTestData(256 << 20)
	for _, level := range []int{3, 12} {
		for _, threads := range []*int{nil, new(1), new(2), new(4), new(0)} {
			comp := New(Zstd).SetLevel(level)
			name := fmt.Sprintf("level=%d/threads=default", level)
			if threads != nil {
				comp = comp.SetThreads(*threads)
				name = fmt.Sprintf("level=%d/threads=%d", level, *threads)
			}
			b.Run(name, func(b *testing.B) {
				compress, _ := Zstd.Compress(b.Context(), comp)
				var cw countingWriter
				b.SetBytes(int64(len(src)))
				for b.Loop() {
					cw.n = 0
					w, err := compress(&cw, ocispecs.MediaTypeImageLayerZstd)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := w.Write(src); err != nil {
						b.Fatal(err)
					}
					if err := w.Close(); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(cw.n)*100/float64(len(src)), "ratio%")
			})
		}
	}
}

type countingWriter struct {
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// zstdCompress compresses src with the zstd compressor for comp, writing the
// input in chunks of chunkSize bytes (all at once when chunkSize is 0).
func zstdCompress(t *testing.T, comp Config, src []byte, chunkSize int) []byte {
	t.Helper()
	compress, _ := Zstd.Compress(t.Context(), comp)
	var buf bytes.Buffer
	w, err := compress(&buf, ocispecs.MediaTypeImageLayerZstd)
	require.NoError(t, err)
	if chunkSize <= 0 || chunkSize > len(src) {
		chunkSize = len(src)
	}
	for len(src) > 0 {
		n := min(chunkSize, len(src))
		_, err := w.Write(src[:n])
		require.NoError(t, err)
		src = src[n:]
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func zstdDecompress(t *testing.T, data []byte) []byte {
	t.Helper()
	r, err := zstd.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	defer r.Close()
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	if out == nil {
		out = []byte{}
	}
	return out
}

// zstdTestData returns n bytes of compressible but non-repetitive data.
func zstdTestData(n int) []byte {
	words := []string{"buildkit ", "layer ", "zstd ", "compression ", "threads ", "content ", "store ", "digest ", "\n"}
	buf := make([]byte, 0, n+16)
	x := uint32(1)
	for len(buf) < n {
		x = x*1664525 + 1013904223 // LCG
		buf = append(buf, words[(x>>28)%uint32(len(words))]...)
		buf = append(buf, byte('0'+x%10))
	}
	return buf[:n]
}
