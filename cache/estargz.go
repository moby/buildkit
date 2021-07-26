package cache

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/stargz-snapshotter/estargz"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

var eStargzAnnotations = []string{estargz.TOCJSONDigestAnnotation, estargz.StoreUncompressedSizeAnnotation}

// writeEStargz writes the passed blobs stream as an eStargz-compressed blob.
// getAnnotations function returns all eStargz annotations.
func writeEStargz() (compressorFunc compressor, finalize func(context.Context, content.Store) (map[string]string, error)) {
	annotations := make(map[string]string)
	var bInfo blobInfo
	var mu sync.Mutex
	return func(dest io.Writer, requiredMediaType string) (io.WriteCloser, error) {
			if !isGzipCompressedType(requiredMediaType) {
				return nil, fmt.Errorf("unsupported media type for estargz compressor %q", requiredMediaType)
			}
			done := make(chan struct{})
			pr, pw := io.Pipe()
			go func() {
				defer close(done)
				defer pr.Close()
				cw, bInfoCh := calculateBlob()
				defer cw.Close()
				w := estargz.NewWriter(io.MultiWriter(dest, cw))
				if err := w.AppendTar(pr); err != nil {
					pr.CloseWithError(err)
					return
				}
				tocDgst, err := w.Close()
				if err != nil {
					pr.CloseWithError(err)
					return
				}
				if err := cw.Close(); err != nil {
					pr.CloseWithError(err)
					return
				}
				mu.Lock()
				bInfo = <-bInfoCh
				annotations[estargz.TOCJSONDigestAnnotation] = tocDgst.String()
				annotations[estargz.StoreUncompressedSizeAnnotation] = fmt.Sprintf("%d", bInfo.uncompressedSize)
				mu.Unlock()
			}()
			return &writeCloser{pw, func() error {
				<-done // wait until the write completes
				return nil
			}}, nil
		}, func(ctx context.Context, cs content.Store) (map[string]string, error) {
			a := make(map[string]string)
			mu.Lock()
			bInfo := bInfo
			for k, v := range annotations {
				a[k] = v
			}
			mu.Unlock()
			info, err := cs.Info(ctx, bInfo.compressedDigest)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get info from content store")
			}
			if info.Labels == nil {
				info.Labels = make(map[string]string)
			}
			info.Labels[containerdUncompressed] = bInfo.uncompressedDigest.String()
			if _, err := cs.Update(ctx, info, "labels."+containerdUncompressed); err != nil {
				return nil, err
			}
			a[containerdUncompressed] = bInfo.uncompressedDigest.String()
			return a, nil
		}
}

type writeCloser struct {
	io.WriteCloser
	closeFunc func() error
}

func (wc *writeCloser) Close() error {
	err1 := wc.WriteCloser.Close()
	err2 := wc.closeFunc()
	if err1 != nil {
		return errors.Wrapf(err1, "failed to close: %v", err2)
	}
	return err2
}

type counter struct {
	n  int64
	mu sync.Mutex
}

func (c *counter) Write(p []byte) (n int, err error) {
	c.mu.Lock()
	c.n += int64(len(p))
	c.mu.Unlock()
	return len(p), nil
}

func (c *counter) size() (n int64) {
	c.mu.Lock()
	n = c.n
	c.mu.Unlock()
	return
}

type blobInfo struct {
	compressedDigest   digest.Digest
	uncompressedDigest digest.Digest
	uncompressedSize   int64
}

func calculateBlob() (io.WriteCloser, chan blobInfo) {
	res := make(chan blobInfo)
	pr, pw := io.Pipe()
	go func() {
		defer pr.Close()
		c := new(counter)
		dgstr := digest.Canonical.Digester()
		diffID := digest.Canonical.Digester()
		decompressR, err := compression.DecompressStream(io.TeeReader(pr, dgstr.Hash()))
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
		res <- blobInfo{dgstr.Digest(), diffID.Digest(), c.size()}

	}()
	return pw, res
}
