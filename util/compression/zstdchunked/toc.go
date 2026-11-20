package zstdchunked

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/vbatts/tar-split/archive/tar"
)

const (
	// ManifestChecksumAnnotation is the layer annotation that contains the
	// digest of the compressed TOC.
	ManifestChecksumAnnotation = "io.github.containers.zstd-chunked.manifest-checksum"

	// ManifestPositionAnnotation is the layer annotation that contains the
	// position of the TOC inside the blob, in the format
	// <offset>:<length>:<uncompressed length>:<type>.
	ManifestPositionAnnotation = "io.github.containers.zstd-chunked.manifest-position"

	// TarSplitPositionAnnotation is the layer annotation that contains the
	// position of the tar-split data inside the blob, in the format
	// <offset>:<length>:<uncompressed length>.
	TarSplitPositionAnnotation = "io.github.containers.zstd-chunked.tarsplit-position"

	// FooterSize is the size of the footer payload at the end of the blob,
	// not including the header of the skippable frame it is stored in.
	FooterSize = 64

	// manifestTypeCRFS is the only TOC format defined by the spec. The TOC is
	// compatible with the CRFS one.
	manifestTypeCRFS = 1

	// skippableFrameHeaderSize is the size of the magic number and the frame
	// size that prefix every zstd skippable frame.
	skippableFrameHeaderSize = 8

	// paxSchilyXattr is the PAX record prefix used for extended attributes.
	paxSchilyXattr = "SCHILY.xattr."
)

// Annotations is the list of layer annotations set for zstd:chunked blobs.
var Annotations = []string{
	ManifestChecksumAnnotation,
	ManifestPositionAnnotation,
	TarSplitPositionAnnotation,
}

var (
	// skippableFrameMagic is the magic number of a zstd skippable frame.
	// https://www.rfc-editor.org/rfc/rfc8878#section-3.1.2
	skippableFrameMagic = []byte{0x50, 0x2a, 0x4d, 0x18}

	// footerMagic is the magic number stored at the end of the footer.
	footerMagic = []byte{0x47, 0x4e, 0x55, 0x6c, 0x49, 0x6e, 0x55, 0x78}
)

// toc is the table of contents of a zstd:chunked blob.
type toc struct {
	// Version is currently expected to be 1.
	Version int `json:"version"`
	// Entries is the list of file metadata, in the same order as the tar
	// stream.
	Entries []fileMetadata `json:"entries"`
	// TarSplitDigest is the digest of the compressed tar-split data.
	TarSplitDigest digest.Digest `json:"tarSplitDigest,omitempty"`
}

// fileMetadata is an entry in the TOC.
//
// Regular files may be represented as a sequence of chunks. In that case the
// file is described by an initial typeReg entry, followed by typeChunk entries
// that only set the Type, Name and Chunk* fields. If there is at least one
// typeChunk entry, the Chunk* fields are set in all of these entries,
// including the initial typeReg one.
type fileMetadata struct {
	Type       string            `json:"type"`
	Name       string            `json:"name"`
	Linkname   string            `json:"linkName,omitempty"`
	Mode       int64             `json:"mode,omitempty"`
	Size       int64             `json:"size,omitempty"`
	UID        int               `json:"uid,omitempty"`
	GID        int               `json:"gid,omitempty"`
	ModTime    *time.Time        `json:"modtime,omitempty"`
	AccessTime *time.Time        `json:"accesstime,omitempty"`
	ChangeTime *time.Time        `json:"changetime,omitempty"`
	Devmajor   int64             `json:"devMajor,omitempty"`
	Devminor   int64             `json:"devMinor,omitempty"`
	Xattrs     map[string]string `json:"xattrs,omitempty"`
	// Digest is the digest of the file contents. It is empty for empty
	// files.
	Digest    string `json:"digest,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	EndOffset int64  `json:"endOffset,omitempty"`

	ChunkSize   int64  `json:"chunkSize,omitempty"`
	ChunkOffset int64  `json:"chunkOffset,omitempty"`
	ChunkDigest string `json:"chunkDigest,omitempty"`
	ChunkType   string `json:"chunkType,omitempty"`
}

const (
	chunkTypeData  = ""
	chunkTypeZeros = "zeros"
)

const (
	typeReg     = "reg"
	typeLink    = "hardlink"
	typeChar    = "char"
	typeBlock   = "block"
	typeDir     = "dir"
	typeFifo    = "fifo"
	typeSymlink = "symlink"
	typeChunk   = "chunk"
)

var tarTypes = map[byte]string{
	tar.TypeReg:     typeReg,
	tar.TypeLink:    typeLink,
	tar.TypeChar:    typeChar,
	tar.TypeBlock:   typeBlock,
	tar.TypeDir:     typeDir,
	tar.TypeFifo:    typeFifo,
	tar.TypeSymlink: typeSymlink,
}

// newFileMetadata creates the TOC entry for hdr. The caller is responsible
// for setting the digest, offsets and chunk information.
func newFileMetadata(hdr *tar.Header) (fileMetadata, error) {
	typ, ok := tarTypes[hdr.Typeflag]
	if !ok {
		return fileMetadata{}, errors.Errorf("unsupported tar entry type %q for %q", hdr.Typeflag, hdr.Name)
	}
	xattrs := make(map[string]string)
	for k, v := range hdr.PAXRecords {
		xattrKey, ok := strings.CutPrefix(k, paxSchilyXattr)
		if !ok {
			continue
		}
		xattrs[xattrKey] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	return fileMetadata{
		Type:       typ,
		Name:       hdr.Name,
		Linkname:   hdr.Linkname,
		Mode:       hdr.Mode,
		Size:       hdr.Size,
		UID:        hdr.Uid,
		GID:        hdr.Gid,
		ModTime:    timeIfNotZero(hdr.ModTime),
		AccessTime: timeIfNotZero(hdr.AccessTime),
		ChangeTime: timeIfNotZero(hdr.ChangeTime),
		Devmajor:   hdr.Devmajor,
		Devminor:   hdr.Devminor,
		Xattrs:     xattrs,
	}, nil
}

func timeIfNotZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// tarSplitData is the compressed tar-split data of a blob.
type tarSplitData struct {
	data             []byte
	digest           digest.Digest
	uncompressedSize int64
}

// footer is the data stored in the last skippable frame of a blob.
type footer struct {
	manifestType uint64

	offset             uint64
	lengthCompressed   uint64
	lengthUncompressed uint64

	offsetTarSplit             uint64
	lengthCompressedTarSplit   uint64
	lengthUncompressedTarSplit uint64
}

func (f footer) marshal() []byte {
	b := make([]byte, FooterSize)
	binary.LittleEndian.PutUint64(b[8*0:], f.offset)
	binary.LittleEndian.PutUint64(b[8*1:], f.lengthCompressed)
	binary.LittleEndian.PutUint64(b[8*2:], f.lengthUncompressed)
	binary.LittleEndian.PutUint64(b[8*3:], f.manifestType)
	binary.LittleEndian.PutUint64(b[8*4:], f.offsetTarSplit)
	binary.LittleEndian.PutUint64(b[8*5:], f.lengthCompressedTarSplit)
	binary.LittleEndian.PutUint64(b[8*6:], f.lengthUncompressedTarSplit)
	copy(b[8*7:], footerMagic)
	return b
}

func unmarshalFooter(b []byte) (footer, error) {
	if len(b) != FooterSize {
		return footer{}, errors.Errorf("invalid zstd:chunked footer size %d", len(b))
	}
	if !bytes.Equal(b[8*7:], footerMagic) {
		return footer{}, errors.New("invalid zstd:chunked footer magic")
	}
	f := footer{
		offset:                     binary.LittleEndian.Uint64(b[8*0:]),
		lengthCompressed:           binary.LittleEndian.Uint64(b[8*1:]),
		lengthUncompressed:         binary.LittleEndian.Uint64(b[8*2:]),
		manifestType:               binary.LittleEndian.Uint64(b[8*3:]),
		offsetTarSplit:             binary.LittleEndian.Uint64(b[8*4:]),
		lengthCompressedTarSplit:   binary.LittleEndian.Uint64(b[8*5:]),
		lengthUncompressedTarSplit: binary.LittleEndian.Uint64(b[8*6:]),
	}
	if f.manifestType != manifestTypeCRFS {
		return footer{}, errors.Errorf("unsupported zstd:chunked manifest type %d", f.manifestType)
	}
	return f, nil
}

// HasFooter reports whether the blob of the given size readable through r
// ends with a zstd:chunked footer.
func HasFooter(r io.ReaderAt, size int64) bool {
	const n = skippableFrameHeaderSize + FooterSize
	if size < n {
		return false
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, size-n); err != nil {
		return false
	}
	if !bytes.Equal(b[:4], skippableFrameMagic) || binary.LittleEndian.Uint32(b[4:8]) != FooterSize {
		return false
	}
	f, err := unmarshalFooter(b[skippableFrameHeaderSize:])
	if err != nil {
		return false
	}
	// the TOC must be located inside the blob, before the footer
	return f.offset+f.lengthCompressed <= uint64(size-n)
}

func writeSkippableFrame(dest io.Writer, data []byte) error {
	if uint64(len(data)) > uint64(^uint32(0)) {
		return errors.Errorf("zstd skippable frame too large: %d bytes", len(data))
	}
	hdr := make([]byte, skippableFrameHeaderSize)
	copy(hdr, skippableFrameMagic)
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(data)))
	if _, err := dest.Write(hdr); err != nil {
		return err
	}
	_, err := dest.Write(data)
	return err
}

// writeManifest appends the TOC, the tar-split data and the footer to dest,
// which is assumed to be positioned at offset in the blob. It returns the
// layer annotations describing the written data.
func writeManifest(dest io.Writer, offset uint64, ts *tarSplitData, entries []fileMetadata, newEncoder encoderFunc) (map[string]string, error) {
	manifestOffset := offset + skippableFrameHeaderSize

	manifest, err := json.Marshal(toc{
		Version:        1,
		Entries:        entries,
		TarSplitDigest: ts.digest,
	})
	if err != nil {
		return nil, err
	}

	var compressedBuffer bytes.Buffer
	zw, err := newEncoder(&compressedBuffer)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(manifest); err != nil {
		zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	compressedManifest := compressedBuffer.Bytes()

	if err := writeSkippableFrame(dest, compressedManifest); err != nil {
		return nil, err
	}

	tarSplitOffset := manifestOffset + uint64(len(compressedManifest)) + skippableFrameHeaderSize
	if err := writeSkippableFrame(dest, ts.data); err != nil {
		return nil, err
	}

	f := footer{
		manifestType:               manifestTypeCRFS,
		offset:                     manifestOffset,
		lengthCompressed:           uint64(len(compressedManifest)),
		lengthUncompressed:         uint64(len(manifest)),
		offsetTarSplit:             tarSplitOffset,
		lengthCompressedTarSplit:   uint64(len(ts.data)),
		lengthUncompressedTarSplit: uint64(ts.uncompressedSize),
	}
	if err := writeSkippableFrame(dest, f.marshal()); err != nil {
		return nil, err
	}

	return map[string]string{
		ManifestChecksumAnnotation: digest.FromBytes(compressedManifest).String(),
		ManifestPositionAnnotation: fmt.Sprintf("%d:%d:%d:%d", manifestOffset, len(compressedManifest), len(manifest), manifestTypeCRFS),
		TarSplitPositionAnnotation: fmt.Sprintf("%d:%d:%d", tarSplitOffset, len(ts.data), ts.uncompressedSize),
	}, nil
}
