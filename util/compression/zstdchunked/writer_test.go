package zstdchunked

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	"github.com/vbatts/tar-split/tar/asm"
	"github.com/vbatts/tar-split/tar/storage"
)

type testEntry struct {
	hdr     tar.Header
	payload []byte
}

func randomBytes(t *testing.T, seed int64, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.New(rand.NewSource(seed)).Read(b) //nolint:gosec // deterministic test data
	require.NoError(t, err)
	return b
}

func testEntries(t *testing.T) []testEntry {
	modTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	// a file with a hole large enough to be detected as a chunk of zeros
	sparse := append(append(randomBytes(t, 1, 3000), make([]byte, 4*holesThreshold)...), randomBytes(t, 2, 3000)...)
	return []testEntry{
		{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "dir/", Mode: 0o755, ModTime: modTime}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "dir/empty", Mode: 0o644, ModTime: modTime}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "dir/small", Mode: 0o600, Uid: 1000, Gid: 1000, ModTime: modTime}, payload: []byte("hello world\n")},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "dir/large", Mode: 0o644, ModTime: modTime}, payload: randomBytes(t, 3, 1<<20)},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "dir/sparse", Mode: 0o644, ModTime: modTime}, payload: sparse},
		{hdr: tar.Header{
			Typeflag:   tar.TypeReg,
			Name:       "dir/xattr",
			Mode:       0o644,
			ModTime:    modTime,
			Format:     tar.FormatPAX,
			PAXRecords: map[string]string{"SCHILY.xattr.user.foo": "bar"},
		}, payload: []byte("xattr")},
		{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "dir/symlink", Linkname: "small", ModTime: modTime}},
		{hdr: tar.Header{Typeflag: tar.TypeLink, Name: "dir/hardlink", Linkname: "dir/small", ModTime: modTime}},
		{hdr: tar.Header{Typeflag: tar.TypeFifo, Name: "dir/fifo", Mode: 0o644, ModTime: modTime}},
	}
}

func buildTar(t *testing.T, entries []testEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e.hdr
		hdr.Size = int64(len(e.payload))
		require.NoError(t, tw.WriteHeader(&hdr))
		_, err := tw.Write(e.payload)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	// some tar writers emit more padding than the two end-of-archive
	// blocks; make sure it is preserved
	buf.Write(make([]byte, 1024))
	return buf.Bytes()
}

func compress(t *testing.T, tarData []byte) ([]byte, map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, zstd.SpeedDefault)
	require.NoError(t, err)
	// write in small pieces to exercise the pipe
	_, err = io.CopyBuffer(w, bytes.NewReader(tarData), make([]byte, 777))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes(), w.Annotations()
}

func decompressAll(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := zstd.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	defer zr.Close()
	out, err := io.ReadAll(zr)
	require.NoError(t, err)
	return out
}

// parsePosition parses an annotation value made of integers separated by ":".
func parsePosition(t *testing.T, s string, n int) []uint64 {
	t.Helper()
	parts := strings.Split(s, ":")
	require.Len(t, parts, n)
	res := make([]uint64, n)
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 10, 64)
		require.NoError(t, err)
		res[i] = v
	}
	return res
}

func TestWriterRoundTrip(t *testing.T) {
	tarData := buildTar(t, testEntries(t))
	blob, _ := compress(t, tarData)

	// a decoder that is not aware of zstd:chunked must return the
	// original tar stream unmodified
	require.Equal(t, tarData, decompressAll(t, blob))
	require.True(t, HasFooter(bytes.NewReader(blob), int64(len(blob))))
}

func TestWriterTOC(t *testing.T) {
	entries := testEntries(t)
	tarData := buildTar(t, entries)
	blob, annotations := compress(t, tarData)

	require.Len(t, annotations, len(Annotations))
	for _, k := range Annotations {
		require.Contains(t, annotations, k)
	}

	// footer and annotations must agree
	footerFrame := blob[len(blob)-skippableFrameHeaderSize-FooterSize:]
	require.Equal(t, skippableFrameMagic, footerFrame[:4])
	require.Equal(t, uint32(FooterSize), binary.LittleEndian.Uint32(footerFrame[4:8]))
	f, err := unmarshalFooter(footerFrame[skippableFrameHeaderSize:])
	require.NoError(t, err)

	manifestPos := parsePosition(t, annotations[ManifestPositionAnnotation], 4)
	require.Equal(t, []uint64{f.offset, f.lengthCompressed, f.lengthUncompressed, manifestTypeCRFS}, manifestPos)
	tarSplitPos := parsePosition(t, annotations[TarSplitPositionAnnotation], 3)
	require.Equal(t, []uint64{f.offsetTarSplit, f.lengthCompressedTarSplit, f.lengthUncompressedTarSplit}, tarSplitPos)

	compressedManifest := blob[f.offset : f.offset+f.lengthCompressed]
	require.Equal(t, digest.FromBytes(compressedManifest).String(), annotations[ManifestChecksumAnnotation])

	manifest := decompressAll(t, compressedManifest)
	require.Len(t, manifest, int(f.lengthUncompressed))
	var parsed toc
	require.NoError(t, json.Unmarshal(manifest, &parsed))
	require.Equal(t, 1, parsed.Version)

	// every file must be described in the TOC, and its payload must be
	// retrievable from the blob using only the TOC
	byName := map[string][]fileMetadata{}
	for _, e := range parsed.Entries {
		byName[e.Name] = append(byName[e.Name], e)
	}
	for _, e := range entries {
		tocEntries := byName[e.hdr.Name]
		require.NotEmpty(t, tocEntries, e.hdr.Name)
		main := tocEntries[0]
		require.Equal(t, tarTypes[e.hdr.Typeflag], main.Type, e.hdr.Name)
		require.Equal(t, e.hdr.Linkname, main.Linkname, e.hdr.Name)
		require.Equal(t, int64(len(e.payload)), main.Size, e.hdr.Name)
		require.Equal(t, e.hdr.Mode, main.Mode, e.hdr.Name)
		require.Equal(t, e.hdr.Uid, main.UID, e.hdr.Name)
		require.Equal(t, e.hdr.Gid, main.GID, e.hdr.Name)
		require.NotNil(t, main.ModTime, e.hdr.Name)
		require.True(t, e.hdr.ModTime.Equal(*main.ModTime), e.hdr.Name)

		if len(e.payload) == 0 {
			require.Empty(t, main.Digest, e.hdr.Name)
			continue
		}
		require.Equal(t, digest.FromBytes(e.payload).String(), main.Digest, e.hdr.Name)

		if len(tocEntries) == 1 {
			require.Positive(t, main.Offset, e.hdr.Name)
			require.Greater(t, main.EndOffset, main.Offset, e.hdr.Name)
			require.Equal(t, e.payload, decompressAll(t, blob[main.Offset:main.EndOffset]), e.hdr.Name)
			continue
		}

		// chunked file: every chunk is an independent frame
		var payload []byte
		for i, c := range tocEntries {
			if i > 0 {
				require.Equal(t, typeChunk, c.Type, e.hdr.Name)
			}
			require.Equal(t, int64(len(payload)), c.ChunkOffset, e.hdr.Name)
			end := main.EndOffset
			if i+1 < len(tocEntries) {
				end = tocEntries[i+1].Offset
			}
			data := decompressAll(t, blob[c.Offset:end])
			require.Len(t, data, int(c.ChunkSize), e.hdr.Name)
			require.Equal(t, digest.FromBytes(data).String(), c.ChunkDigest, e.hdr.Name)
			if c.ChunkType == chunkTypeZeros {
				require.Equal(t, make([]byte, len(data)), data, e.hdr.Name)
			}
			payload = append(payload, data...)
		}
		require.Equal(t, e.payload, payload, e.hdr.Name)
	}

	require.Greater(t, len(byName["dir/large"]), 1, "large random file is expected to be split in chunks")
	var zeros int
	for _, c := range byName["dir/sparse"] {
		if c.ChunkType == chunkTypeZeros {
			zeros++
			require.Equal(t, 4*holesThreshold, c.ChunkSize)
		}
	}
	require.Equal(t, 1, zeros, "hole in sparse file is expected to be a chunk of zeros")

	xattrs := byName["dir/xattr"][0].Xattrs
	require.Equal(t, map[string]string{"user.foo": base64.StdEncoding.EncodeToString([]byte("bar"))}, xattrs)

	// the tar-split data must allow reassembling the original tar stream
	// from the file payloads
	compressedTarSplit := blob[f.offsetTarSplit : f.offsetTarSplit+f.lengthCompressedTarSplit]
	require.Equal(t, parsed.TarSplitDigest, digest.FromBytes(compressedTarSplit))
	tarSplit := decompressAll(t, compressedTarSplit)
	require.Len(t, tarSplit, int(f.lengthUncompressedTarSplit))

	payloads := map[string][]byte{}
	for _, e := range entries {
		payloads[e.hdr.Name] = e.payload
	}
	rc := asm.NewOutputTarStream(fileGetter(payloads), storage.NewJSONUnpacker(bytes.NewReader(tarSplit)))
	defer rc.Close()
	reassembled, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, tarData, reassembled)
}

type fileGetter map[string][]byte

func (g fileGetter) Get(name string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(g[name])), nil
}

func TestWriterEmptyTar(t *testing.T) {
	tarData := buildTar(t, nil)
	blob, annotations := compress(t, tarData)
	require.Equal(t, tarData, decompressAll(t, blob))
	require.True(t, HasFooter(bytes.NewReader(blob), int64(len(blob))))
	require.NotEmpty(t, annotations[ManifestChecksumAnnotation])
}

func TestWriterInvalidTar(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, zstd.SpeedDefault)
	require.NoError(t, err)
	// a corrupted header must not be accepted silently
	_, _ = w.Write(bytes.Repeat([]byte{0xff}, 1024))
	require.Error(t, w.Close())
	require.Nil(t, w.Annotations())
}

func TestWriterDestinationError(t *testing.T) {
	w, err := NewWriter(errWriter{}, zstd.SpeedDefault)
	require.NoError(t, err)
	_, _ = io.Copy(w, bytes.NewReader(buildTar(t, testEntries(t))))
	require.ErrorIs(t, w.Close(), errTestWrite)
}

var errTestWrite = io.ErrShortWrite

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errTestWrite }

func TestHasFooter(t *testing.T) {
	var plain bytes.Buffer
	zw, err := zstd.NewWriter(&plain)
	require.NoError(t, err)
	_, err = zw.Write(buildTar(t, testEntries(t)))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.False(t, HasFooter(bytes.NewReader(plain.Bytes()), int64(plain.Len())))
	require.False(t, HasFooter(bytes.NewReader(nil), 0))

	blob, _ := compress(t, buildTar(t, testEntries(t)))
	require.True(t, HasFooter(bytes.NewReader(blob), int64(len(blob))))

	// a truncated blob must not be detected
	truncated := blob[:len(blob)-1]
	require.False(t, HasFooter(bytes.NewReader(truncated), int64(len(truncated))))
}

func TestHolesFinder(t *testing.T) {
	data := []byte("\x00\x00\x00\x00\x00FOO\x00\x00\x00\x00\x00")

	hf := &holesFinder{
		threshold: 2,
		reader:    bufio.NewReader(bytes.NewReader(data)),
	}

	hole, _, err := hf.readByte()
	require.NoError(t, err)
	require.Equal(t, int64(5), hole)

	for _, e := range []byte("FOO") {
		hole, c, err := hf.readByte()
		require.NoError(t, err)
		require.Zero(t, hole)
		require.Equal(t, e, c)
	}

	hole, _, err = hf.readByte()
	require.NoError(t, err)
	require.Equal(t, int64(5), hole)

	_, _, err = hf.readByte()
	require.ErrorIs(t, err, io.EOF)

	// zeros below the threshold are returned as regular bytes
	hf = &holesFinder{
		threshold: 1000,
		reader:    bufio.NewReader(bytes.NewReader(data[:5])),
	}
	for range 5 {
		hole, b, err := hf.readByte()
		require.NoError(t, err)
		require.Zero(t, hole)
		require.Zero(t, b)
	}
	_, _, err = hf.readByte()
	require.ErrorIs(t, err, io.EOF)
}
