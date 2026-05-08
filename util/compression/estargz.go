package compression

import (
	"archive/tar"
	"bytes"
	stdgzip "compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"strconv"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	cdcompression "github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/stargz-snapshotter/estargz"
	gzip "github.com/klauspost/compress/gzip"
	"github.com/moby/buildkit/util/iohelper"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

var EStargzAnnotations = []string{estargz.TOCJSONDigestAnnotation, estargz.StoreUncompressedSizeAnnotation}

const estargzLabel = "buildkit.io/compression/estargz"

// defaultEstargzCompressionLevel is the gzip level used when comp.Level is unset.
// L4 sits at the knee of the speed/size curve for our ML payloads: significantly
// faster encode than L6 with a small size penalty that is near-zero on the
// already-incompressible content (Python wheels, native libs, model weights)
// that dominates our largest layers. Combined with the klauspost gzip
// implementation below this is materially faster than upstream's stdlib L6.
const defaultEstargzCompressionLevel = 4

func (c estargzType) Compress(ctx context.Context, comp Config) (compressorFunc Compressor, finalize Finalizer) {
	var cInfo *compressionInfo
	var writeErr error
	var mu sync.Mutex
	return func(dest io.Writer, requiredMediaType string) (io.WriteCloser, error) {
			ct, err := FromMediaType(requiredMediaType)
			if err != nil {
				return nil, err
			}
			if ct != Gzip {
				return nil, errors.Errorf("unsupported media type for estargz compressor %q", requiredMediaType)
			}
			done := make(chan struct{})
			pr, pw := io.Pipe()
			go func() (retErr error) {
				defer close(done)
				defer func() {
					if retErr != nil {
						mu.Lock()
						writeErr = retErr
						mu.Unlock()
					}
				}()

				blobInfoW, bInfoCh := calculateBlobInfo()
				defer blobInfoW.Close()
				level := defaultEstargzCompressionLevel
				if comp.Level != nil {
					level = *comp.Level
				}
				w := estargz.NewWriterWithCompressor(io.MultiWriter(dest, blobInfoW), newKlauspostGzipCompressor(level))

				// Using lossless API here to make sure that decompressEStargz provides the exact
				// same tar as the original.
				//
				// Note that we don't support eStragz compression for tar that contains a file named
				// `stargz.index.json` because we cannot create eStargz in loseless way for such blob
				// (we must overwrite stargz.index.json file).
				if err := w.AppendTarLossLess(pr); err != nil {
					pr.CloseWithError(err)
					return err
				}
				tocDgst, err := w.Close()
				if err != nil {
					pr.CloseWithError(err)
					return err
				}
				if err := blobInfoW.Close(); err != nil {
					pr.CloseWithError(err)
					return err
				}
				bInfo := <-bInfoCh
				mu.Lock()
				cInfo = &compressionInfo{bInfo, tocDgst}
				mu.Unlock()
				pr.Close()
				return nil
			}()
			return &iohelper.WriteCloser{WriteCloser: pw, CloseFunc: func() error {
				<-done // wait until the write completes
				return nil
			}}, nil
		}, func(ctx context.Context, cs content.Store) (map[string]string, error) {
			mu.Lock()
			cInfo, writeErr := cInfo, writeErr
			mu.Unlock()
			if cInfo == nil {
				if writeErr != nil {
					return nil, errors.Wrapf(writeErr, "cannot finalize due to write error")
				}
				return nil, errors.Errorf("cannot finalize (reason unknown)")
			}

			// Fill necessary labels
			info, err := cs.Info(ctx, cInfo.compressedDigest)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get info from content store")
			}
			if info.Labels == nil {
				info.Labels = make(map[string]string)
			}
			info.Labels[labels.LabelUncompressed] = cInfo.uncompressedDigest.String()
			if _, err := cs.Update(ctx, info, "labels."+labels.LabelUncompressed); err != nil {
				return nil, err
			}

			// Fill annotations
			a := make(map[string]string)
			a[estargz.TOCJSONDigestAnnotation] = cInfo.tocDigest.String()
			a[estargz.StoreUncompressedSizeAnnotation] = fmt.Sprintf("%d", cInfo.uncompressedSize)
			a[labels.LabelUncompressed] = cInfo.uncompressedDigest.String()
			return a, nil
		}
}

func (c estargzType) Decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	return decompress(ctx, cs, desc)
}

func (c estargzType) NeedsConversion(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (bool, error) {
	esgz, err := c.Is(ctx, cs, desc.Digest)
	if err != nil {
		return false, err
	}
	if !images.IsLayerType(desc.MediaType) || esgz {
		return false, nil
	}
	return true, nil
}

func (c estargzType) NeedsComputeDiffBySelf(comp Config) bool {
	return true
}

func (c estargzType) OnlySupportOCITypes() bool {
	return true
}

func (c estargzType) MediaType() string {
	return ocispecs.MediaTypeImageLayerGzip
}

func (c estargzType) String() string {
	return "estargz"
}

// isEStargz returns true when the specified digest of content exists in
// the content store and it's eStargz.
func (c estargzType) Is(ctx context.Context, cs content.Store, dgst digest.Digest) (bool, error) {
	info, err := cs.Info(ctx, dgst)
	if err != nil {
		return false, nil
	}
	if isEsgzStr, ok := info.Labels[estargzLabel]; ok {
		if isEsgz, err := strconv.ParseBool(isEsgzStr); err == nil {
			return isEsgz, nil
		}
	}

	res := func() bool {
		r, err := cs.ReaderAt(ctx, ocispecs.Descriptor{Digest: dgst})
		if err != nil {
			return false
		}
		defer r.Close()
		sr := io.NewSectionReader(r, 0, r.Size())

		// Does this have the footer?
		tocOffset, _, err := estargz.OpenFooter(sr)
		if err != nil {
			return false
		}

		// Is TOC the final entry?
		decompressor := new(estargz.GzipDecompressor)
		rr, err := decompressor.Reader(io.NewSectionReader(sr, tocOffset, sr.Size()-tocOffset))
		if err != nil {
			return false
		}
		tr := tar.NewReader(rr)
		h, err := tr.Next()
		if err != nil {
			return false
		}
		if h.Name != estargz.TOCTarName {
			return false
		}
		if _, err = tr.Next(); !errors.Is(err, io.EOF) { // must be EOF
			return false
		}

		return true
	}()

	if info.Labels == nil {
		info.Labels = make(map[string]string)
	}
	info.Labels[estargzLabel] = strconv.FormatBool(res) // cache the result
	if _, err := cs.Update(ctx, info, "labels."+estargzLabel); err != nil {
		return false, err
	}

	return res, nil
}

func decompressEStargz(r *io.SectionReader) (io.ReadCloser, error) {
	return estargz.Unpack(r, new(estargz.GzipDecompressor))
}

type compressionInfo struct {
	blobInfo
	tocDigest digest.Digest
}

type blobInfo struct {
	compressedDigest   digest.Digest
	uncompressedDigest digest.Digest
	uncompressedSize   int64
}

func calculateBlobInfo() (io.WriteCloser, chan blobInfo) {
	res := make(chan blobInfo)
	pr, pw := io.Pipe()
	go func() {
		defer pr.Close()
		c := new(iohelper.Counter)
		dgstr := digest.Canonical.Digester()
		diffID := digest.Canonical.Digester()
		decompressR, err := cdcompression.DecompressStream(io.TeeReader(pr, dgstr.Hash()))
		if err != nil {
			pr.CloseWithError(err)
			return
		}
		defer decompressR.Close()
		if _, err := io.Copy(io.MultiWriter(c, diffID.Hash()), decompressR); err != nil {
			pr.CloseWithError(err)
			return
		}
		if err := decompressR.Close(); err != nil {
			pr.CloseWithError(err)
			return
		}
		res <- blobInfo{dgstr.Digest(), diffID.Digest(), c.Size()}
	}()
	return pw, res
}

// klauspostGzipCompressor is an estargz.Compressor that swaps the per-chunk gzip
// encoder for github.com/klauspost/compress/gzip. The on-disk format is unchanged:
// every estargz chunk is still an RFC 1952 gzip stream, so any decompressor
// (stargz-snapshotter, containerd, plain gunzip) reads it unchanged. Encoder is
// roughly 2x faster than stdlib at the same level on our workloads.
type klauspostGzipCompressor struct {
	level int
}

func newKlauspostGzipCompressor(level int) *klauspostGzipCompressor {
	return &klauspostGzipCompressor{level: level}
}

func (c *klauspostGzipCompressor) Writer(w io.Writer) (estargz.WriteFlushCloser, error) {
	return gzip.NewWriterLevel(w, c.level)
}

func (c *klauspostGzipCompressor) WriteTOCAndFooter(w io.Writer, off int64, toc *estargz.JTOC, diffHash hash.Hash) (digest.Digest, error) {
	tocJSON, err := json.MarshalIndent(toc, "", "\t")
	if err != nil {
		return "", err
	}
	gz, _ := gzip.NewWriterLevel(w, c.level)
	gw := io.Writer(gz)
	if diffHash != nil {
		gw = io.MultiWriter(gz, diffHash)
	}
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     estargz.TOCTarName,
		Size:     int64(len(tocJSON)),
	}); err != nil {
		return "", err
	}
	if _, err := tw.Write(tocJSON); err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if _, err := w.Write(klauspostGzipFooterBytes(off)); err != nil {
		return "", err
	}
	return digest.FromBytes(tocJSON), nil
}

// klauspostGzipFooterBytes mirrors estargz/gzip.go's gzipFooterBytes.
// Uses stdlib at NoCompression to guarantee the spec-required 51-byte footer.
// Layout per RFC 1952 subfield: 'S','G' magic + uint16 LE subfield length +
// 16-hex-digit TOC offset + "STARGZ" magic.
func klauspostGzipFooterBytes(tocOff int64) []byte {
	buf := bytes.NewBuffer(make([]byte, 0, estargz.FooterSize))
	gz, _ := stdgzip.NewWriterLevel(buf, stdgzip.NoCompression)
	subfield := fmt.Sprintf("%016xSTARGZ", tocOff)
	extra := make([]byte, 0, 4+len(subfield))
	extra = append(extra, 'S', 'G')
	extra = binary.LittleEndian.AppendUint16(extra, uint16(len(subfield)))
	extra = append(extra, subfield...)
	gz.Extra = extra
	gz.Close()
	if buf.Len() != estargz.FooterSize {
		panic(fmt.Sprintf("footer buffer = %d, not %d", buf.Len(), estargz.FooterSize))
	}
	return buf.Bytes()
}
