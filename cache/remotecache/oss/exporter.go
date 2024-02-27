package oss

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/labels"
	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const (
	ossMaxNumOfChunk        int64 = 9999
	ossMinSizeOfChunk       int64 = 10 * (1 << 20)
	maxParallelChunkUploads uint  = 5
	maxRetryAttempts        uint  = 5
)

type chunk struct {
	offset  int64
	size    int64
	attempt uint
	id      int
}
type chunkUploadResult struct {
	err   error
	chunk *chunk
}

// ResolveCacheExporterFunc for "OSS" cache exporter.
func ResolveCacheExporterFunc() remotecache.ResolveCacheExporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Exporter, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to create azblob config")
		}

		ossClient, err := createOSSClient(config)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to create container client")
		}

		cc := v1.NewCacheChains()
		return &exporter{
			CacheExporterTarget: cc,
			chains:              cc,
			ossClient:           ossClient,
			config:              config,
		}, nil
	}
}

var _ remotecache.Exporter = &exporter{}

type exporter struct {
	solver.CacheExporterTarget
	chains    *v1.CacheChains
	ossClient *ossClient
	config    *Config
}

func (ce *exporter) Name() string {
	return "exporting cache to Alibaba Cloud OSS"
}

func (ce *exporter) Finalize(ctx context.Context) (map[string]string, error) {
	config, descs, err := ce.chains.Marshal(ctx)
	if err != nil {
		return nil, err
	}

	for i, l := range config.Layers {
		dgstPair, ok := descs[l.Blob]
		if !ok {
			return nil, errors.Errorf("missing blob %s", l.Blob)
		}
		if dgstPair.Descriptor.Annotations == nil {
			return nil, errors.Errorf("invalid descriptor without annotations")
		}
		var diffID digest.Digest
		v, ok := dgstPair.Descriptor.Annotations[labels.LabelUncompressed]
		if !ok {
			return nil, errors.Errorf("invalid descriptor without uncompressed annotation")
		}
		dgst, err := digest.Parse(v)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse uncompressed annotation")
		}
		diffID = dgst

		key := ce.ossClient.blobKey(dgstPair.Descriptor.Digest)

		exists, err := blobExists(ce.ossClient, key)
		if err != nil {
			return nil, err
		}

		bklog.G(ctx).Debugf("layers %s exists = %t", key, exists)

		if !exists {
			layerDone := progress.OneOff(ctx, fmt.Sprintf("writing layer %s", l.Blob))
			ra, err := dgstPair.Provider.ReaderAt(ctx, dgstPair.Descriptor)
			if err != nil {
				err = errors.Wrapf(err, "failed to get reader for %s", dgstPair.Descriptor.Digest)
				return nil, layerDone(err)
			}
			if err := ce.uploadBlobIfNotExists(ctx, key, ra); err != nil {
				return nil, layerDone(err)
			}
			_ = layerDone(nil)
		}

		la := &v1.LayerAnnotations{
			DiffID:    diffID,
			Size:      dgstPair.Descriptor.Size,
			MediaType: dgstPair.Descriptor.MediaType,
		}
		if v, ok := dgstPair.Descriptor.Annotations["buildkit/createdat"]; ok {
			var t time.Time
			if err := (&t).UnmarshalText([]byte(v)); err != nil {
				return nil, err
			}
			la.CreatedAt = t.UTC()
		}
		config.Layers[i].Annotations = la
	}

	dt, err := json.Marshal(config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal config")
	}

	for _, name := range ce.config.Names {
		if innerError := ce.uploadManifest(ce.ossClient.manifestKey(name), bytesToReadSeekCloser(dt)); innerError != nil {
			return nil, errors.Wrapf(innerError, "error writing manifest %s", name)
		}
	}

	return nil, nil
}

func (ce *exporter) Config() remotecache.Config {
	return remotecache.Config{
		Compression: compression.New(compression.Default),
	}
}

// uploadManifest uploads a cache manifest to the remote OSS cache.
func (ce *exporter) uploadManifest(manifestKey string, reader io.ReadSeekCloser) error {
	defer reader.Close()
	return ce.ossClient.Bucket.PutObject(manifestKey, reader)
}

// uploadBlobIfNotExists uploads a blob to the remote OSS cache.
func (ce *exporter) uploadBlobIfNotExists(ctx context.Context, blobKey string, reader content.ReaderAt) error {
	var (
		size        = reader.Size()
		chunkSize   = int64(math.Max(float64(ossMinSizeOfChunk), float64(size/ossMaxNumOfChunk)))
		numOfChunks = size / chunkSize
	)
	if size%chunkSize != 0 {
		numOfChunks++
	}

	chunks, err := splitChunks(reader, numOfChunks)
	if err != nil {
		return err
	}

	imur, err := ce.ossClient.Bucket.InitiateMultipartUpload(blobKey)
	if err != nil {
		return err
	}

	var (
		completedParts = make([]oss.UploadPart, numOfChunks)
		chunkUploadCh  = make(chan chunk, numOfChunks)
		resCh          = make(chan chunkUploadResult, numOfChunks)
		cancelCh       = make(chan struct{})
		wg             sync.WaitGroup
	)

	for i := uint(0); i < maxParallelChunkUploads; i++ {
		wg.Add(1)
		go ce.partUploader(ctx, &wg, imur, reader, completedParts, chunkUploadCh, cancelCh, resCh)
	}

	for i := range chunks {
		bklog.G(ctx).Debugf("Triggering chunk upload for offset: %d", chunks[i].offset)
		chunkUploadCh <- chunks[i]
	}

	bklog.G(ctx).Debugf("Triggered chunk upload for all chunks, total: %d", numOfChunks)
	cacheErr := collectChunkUploadError(ctx, chunkUploadCh, resCh, cancelCh, numOfChunks)
	wg.Wait()

	if cacheErr == nil {
		_, err := ce.ossClient.Bucket.CompleteMultipartUpload(imur, completedParts)
		if err != nil {
			return err
		}
		bklog.G(ctx).Debugf("Finishing the multipart upload with upload ID : %s", imur.UploadID)
	} else {
		bklog.G(ctx).Debugf("Aborting the multipart upload with upload ID : %s", imur.UploadID)
		err := ce.ossClient.Bucket.AbortMultipartUpload(imur)
		if err != nil {
			return cacheErr.err
		}
	}
	return nil
}

func (ce *exporter) partUploader(ctx context.Context, wg *sync.WaitGroup, imur oss.InitiateMultipartUploadResult, ra content.ReaderAt, completedParts []oss.UploadPart, chunkUploadCh <-chan chunk, stopCh <-chan struct{}, errCh chan<- chunkUploadResult) {
	defer wg.Done()
	for {
		select {
		case <-stopCh:
			return
		case chunk, ok := <-chunkUploadCh:
			if !ok {
				return
			}
			bklog.G(ctx).Debugf("Uploading chunk with id: %d, offset: %d, size: %d", chunk.id, chunk.offset, chunk.size)
			err := ce.uploadPart(imur, ra, completedParts, chunk.offset, chunk.size, chunk.id)
			errCh <- chunkUploadResult{
				err:   err,
				chunk: &chunk,
			}
		}
	}
}

func (ce *exporter) uploadPart(imur oss.InitiateMultipartUploadResult, ra content.ReaderAt, completedParts []oss.UploadPart, offset, chunkSize int64, number int) error {
	fd := io.NewSectionReader(ra, offset, chunkSize)
	part, err := ce.ossClient.Bucket.UploadPart(imur, fd, chunkSize, number)

	if err == nil {
		completedParts[number-1] = part
	}
	return err
}

// collectChunkUploadError collects the error from all go routine to upload individual chunks
func collectChunkUploadError(ctx context.Context, chunkUploadCh chan<- chunk, resCh <-chan chunkUploadResult, stopCh chan struct{}, noOfChunks int64) *chunkUploadResult {
	remainingChunks := noOfChunks
	bklog.G(ctx).Debugf("No of Chunks:= %d", noOfChunks)
	for chunkRes := range resCh {
		bklog.G(ctx).Debugf("Received chunk result for id: %d, offset: %d", chunkRes.chunk.id, chunkRes.chunk.offset)
		if chunkRes.err != nil {
			bklog.G(ctx).Debugf("Chunk upload failed for id: %d, offset: %d with err: %v", chunkRes.chunk.id, chunkRes.chunk.offset, chunkRes.err)
			if chunkRes.chunk.attempt == maxRetryAttempts {
				bklog.G(ctx).Debugf("Received the chunk upload error even after %d attempts from one of the workers. Sending stop signal to all workers.", chunkRes.chunk.attempt)
				close(stopCh)
				return &chunkRes
			}
			chunk := chunkRes.chunk
			delayTime := 1 << chunk.attempt
			chunk.attempt++
			bklog.G(ctx).Debugf("Will try to upload chunk id: %d, offset: %d at attempt %d  after %d seconds", chunk.id, chunk.offset, chunk.attempt, delayTime)
			time.AfterFunc(time.Duration(delayTime)*time.Second, func() {
				select {
				case <-stopCh:
					return
				default:
					chunkUploadCh <- *chunk
				}
			})
		} else {
			remainingChunks--
			if remainingChunks == 0 {
				bklog.G(ctx).Debugf("Received successful chunk result for all chunks. Stopping workers.")
				close(stopCh)
				break
			}
		}
	}
	return nil
}

func splitChunks(ra content.ReaderAt, chunkNum int64) ([]chunk, error) {
	if chunkNum <= 0 || chunkNum >= ossMaxNumOfChunk {
		return nil, errors.New("chunkNum invalid")
	}
	if chunkNum > ra.Size() {
		return nil, errors.New("chunkNum invalid")
	}

	var chunks []chunk
	var chunk = chunk{}
	var chunkN = chunkNum
	for i := int64(0); i < chunkN; i++ {
		chunk.id = int(i + 1)
		chunk.offset = i * (ra.Size() / chunkN)
		if i == chunkN-1 {
			chunk.size = ra.Size()/chunkN + ra.Size()%chunkN
		} else {
			chunk.size = ra.Size() / chunkN
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

var _ io.ReadSeekCloser = &readSeekCloser{}

type readSeekCloser struct {
	io.Reader
	io.Seeker
	io.Closer
}

func bytesToReadSeekCloser(dt []byte) io.ReadSeekCloser {
	bytesReader := bytes.NewReader(dt)
	return &readSeekCloser{
		Reader: bytesReader,
		Seeker: bytesReader,
		Closer: io.NopCloser(bytesReader),
	}
}
