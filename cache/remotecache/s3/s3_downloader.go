package s3

// To be replaced by direct SDK call when available
// https://github.com/aws/aws-sdk-go-v2/issues/2247

import (
	"context"
	"io"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/containerd/containerd/content"
	"golang.org/x/sync/errgroup"
)

type s3DownloaderInput struct {
	Bucket      *string
	Key         *string
	S3Client    *s3.Client
	Size        int64
	Parallelism int
	PartSize    int
}

type s3Downloader struct {
	input *s3DownloaderInput
}

type chunkStatus struct {
	writeOffset int64
	readOffset  int64
	done        bool
	size        int64
	start       int64
	buffer      []byte
	io.Writer
}

func (chunk *chunkStatus) Write(p []byte) (n int, err error) {
	copied := copy(chunk.buffer[chunk.writeOffset:], p)
	chunk.writeOffset += int64(copied)
	return copied, nil
}

type s3Reader struct {
	content.ReaderAt
	inChan        chan int
	outChan       chan error
	start         int64
	currentOffset int64
	currentChunk  int
	input         *s3DownloaderInput
	ctx           context.Context
	eg            *errgroup.Group
	groupCtx      context.Context
	totalChunk    int
	chunks        []chunkStatus
}

func newDownloader(input *s3DownloaderInput) *s3Downloader {
	if input.Parallelism == 0 {
		input.Parallelism = 4
	}
	if input.PartSize == 0 {
		input.PartSize = 5 * 1024 * 1024
	}
	return &s3Downloader{
		input: input,
	}
}

func (s *s3Downloader) Download(ctx context.Context) (content.ReaderAt, error) {
	reader := &s3Reader{
		input:         s.input,
		currentOffset: -1,
		ctx:           ctx,
	}
	return reader, nil
}

func (r *s3Reader) Size() int64 {
	return r.input.Size
}

func (r *s3Reader) downloadChunk(chunk int) error {
	r.chunks[chunk].start = r.start + int64(chunk)*int64(r.input.PartSize)
	r.chunks[chunk].size = int64(r.input.PartSize)
	if r.chunks[chunk].start+r.chunks[chunk].size > r.input.Size {
		r.chunks[chunk].size = r.input.Size - r.chunks[chunk].start
	}

	r.chunks[chunk].buffer = make([]byte, r.chunks[chunk].size)
	bytesRange := r.buildRange(r.chunks[chunk].start, r.chunks[chunk].size)
	input := &s3.GetObjectInput{
		Bucket: r.input.Bucket,
		Key:    r.input.Key,
		Range:  &bytesRange,
	}
	resp, err := r.input.S3Client.GetObject(r.groupCtx, input)
	if err != nil {
		return err
	}
	n, err := io.Copy(&r.chunks[chunk], resp.Body)
	if err != nil {
		return err
	}
	if err := resp.Body.Close(); err != nil {
		return err
	}
	r.chunks[chunk].size = n
	r.chunks[chunk].done = true
	return nil
}

func (r *s3Reader) buildRange(start int64, size int64) string {
	startRange := strconv.FormatInt(start, 10)
	stopRange := strconv.FormatInt(start+size-1, 10)
	return "bytes=" + startRange + "-" + stopRange
}

func (r *s3Reader) reset(offset int64) error {
	if err := r.Close(); err != nil {
		return err
	}
	r.eg, r.groupCtx = errgroup.WithContext(r.ctx)
	r.totalChunk = int((r.input.Size-offset)/int64(r.input.PartSize)) + 1
	r.chunks = make([]chunkStatus, r.totalChunk)
	r.inChan = make(chan int, r.totalChunk)
	r.outChan = make(chan error, r.totalChunk)
	r.currentOffset = offset
	r.currentChunk = 0
	r.start = offset
	for i := 0; i < r.totalChunk; i++ {
		r.inChan <- i
	}
	for k := 0; k < r.input.Parallelism; k++ {
		r.eg.Go(func() error {
			for item := range r.inChan {
				err := r.downloadChunk(item)
				r.outChan <- err
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	return nil
}

func (r *s3Reader) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= r.input.Size {
		return 0, io.EOF
	}
	if off != r.currentOffset {
		if err := r.reset(off); err != nil {
			return 0, err
		}
	}

	pOffset := 0
	for {
		copied, err := r.readAtOneChunk(p[pOffset:])
		pOffset += copied
		n += copied
		if err != nil {
			return n, err
		}
		if pOffset == len(p) {
			break
		}
	}

	return n, nil
}

func (r *s3Reader) readAtOneChunk(p []byte) (n int, err error) {
	for {
		if r.chunks[r.currentChunk].done {
			break
		}
		err := <-r.outChan
		if err != nil {
			r.Close()
			return 0, err
		}
	}

	copied := copy(p, r.chunks[r.currentChunk].buffer[r.chunks[r.currentChunk].readOffset:])
	r.chunks[r.currentChunk].readOffset += int64(copied)
	r.currentOffset += int64(copied)

	if r.currentOffset >= r.chunks[r.currentChunk].start+r.chunks[r.currentChunk].size {
		r.chunks[r.currentChunk].buffer = nil
		r.currentChunk++
	}

	if r.currentChunk == r.totalChunk {
		r.Close()
		return copied, io.EOF
	}

	return copied, nil
}

func (r *s3Reader) Close() error {
	if r.eg != nil {
		close(r.inChan)
		err := r.eg.Wait()
		close(r.outChan)
		r.eg = nil
		r.currentOffset = -1
		r.inChan = nil
		r.outChan = nil
		r.chunks = nil
		return err
	}
	return nil
}
