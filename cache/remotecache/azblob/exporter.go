package azblob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
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

// ResolveCacheExporterFunc for "azblob" cache exporter.
func ResolveCacheExporterFunc() remotecache.ResolveCacheExporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Exporter, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to create azblob config")
		}

		client, err := newClient(ctx, config)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to create container client")
		}

		cc := v1.NewCacheChains()
		return &exporter{
			CacheExporterTarget: cc,
			chains:              cc,
			client:              client,
			config:              config,
		}, nil
	}
}

var _ remotecache.Exporter = &exporter{}

type exporter struct {
	solver.CacheExporterTarget
	chains *v1.CacheChains
	client *Client
	config *Config
}

func (ce *exporter) Name() string {
	return "exporting cache to Azure Blob Storage"
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

		key := ce.client.blobKey(dgstPair.Descriptor.Digest.String())

		exists, err := ce.client.blobExists(ctx, key)
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
			if err := ce.uploadBlobIfNotExists(ctx, key, content.NewReader(ra)); err != nil {
				return nil, layerDone(err)
			}
			layerDone(nil)
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
		if innerError := ce.uploadManifest(ctx, ce.client.manifestKey(name), bytesToReadSeekCloser(dt)); innerError != nil {
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

// For uploading manifests, use the Upload API which follows "last writer wins" semantics
// This is slightly slower than UploadStream call but is safe to call concurrently from multiple threads. Refer to:
// https://github.com/Azure/azure-sdk-for-go/issues/18490#issuecomment-1170806877
func (ce *exporter) uploadManifest(ctx context.Context, manifestKey string, reader io.ReadSeekCloser) error {
	defer reader.Close()

	ctx, cnclFn := context.WithCancelCause(ctx)
	ctx, _ = context.WithTimeoutCause(ctx, time.Minute*5, errors.WithStack(context.DeadlineExceeded))
	defer cnclFn(errors.WithStack(context.Canceled))

	if _, err := ce.client.UploadStream(ctx, ce.config.Container, manifestKey, reader, nil); err != nil {
		return errors.Wrapf(err, "failed to upload blob %s: %v", manifestKey, err)
	}

	return nil
}

// For uploading blobs, use the UploadStream with access conditions which state that only upload if the blob
// does not already exist. Since blobs are content addressable, this is the right thing to do for blobs and it gives
// a performance improvement over the Upload API used for uploading manifests.
func (ce *exporter) uploadBlobIfNotExists(ctx context.Context, blobKey string, reader io.Reader) error {
	ctx, cnclFn := context.WithCancelCause(ctx)
	ctx, _ = context.WithTimeoutCause(ctx, time.Minute*5, errors.WithStack(context.DeadlineExceeded))
	defer cnclFn(errors.WithStack(context.Canceled))

	eTagAny := azcore.ETagAny
	if _, err := ce.client.UploadStream(ctx, ce.config.Container, blobKey, reader, &azblob.UploadStreamOptions{
		BlockSize:   IOChunkSize,
		Concurrency: IOConcurrency,
		AccessConditions: &azblob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{
				IfNoneMatch: &eTagAny,
			},
		},
	}); err != nil {
		return errors.Wrapf(err, "failed to upload blob %s: %v", blobKey, err)
	}

	return nil
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
