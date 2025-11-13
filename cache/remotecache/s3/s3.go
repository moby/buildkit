package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	cacheimporttypes "github.com/moby/buildkit/cache/remotecache/v1/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

const (
	attrBucket            = "bucket"
	attrRegion            = "region"
	attrPrefix            = "prefix"
	attrManifestsPrefix   = "manifests_prefix"
	attrBlobsPrefix       = "blobs_prefix"
	attrName              = "name"
	attrTouchRefresh      = "touch_refresh"
	attrEndpointURL       = "endpoint_url"
	attrAccessKeyID       = "access_key_id"
	attrSecretAccessKey   = "secret_access_key"
	attrSessionToken      = "session_token"
	attrUsePathStyle      = "use_path_style"
	attrUploadParallelism = "upload_parallelism"
	maxCopyObjectSize     = 5 * 1024 * 1024 * 1024
)

type Config struct {
	Bucket            string
	Region            string
	Prefix            string
	ManifestsPrefix   string
	BlobsPrefix       string
	Names             []string
	TouchRefresh      time.Duration
	EndpointURL       string
	AccessKeyID       string
	SecretAccessKey   string
	SessionToken      string
	UsePathStyle      bool
	UploadParallelism int
}

func getConfig(attrs map[string]string) (Config, error) {
	bucket, ok := attrs[attrBucket]
	if !ok {
		bucket, ok = os.LookupEnv("AWS_BUCKET")
		if !ok {
			return Config{}, errors.Errorf("bucket ($AWS_BUCKET) not set for s3 cache")
		}
	}

	region, ok := attrs[attrRegion]
	if !ok {
		region, _ = os.LookupEnv("AWS_REGION") // optional for minio; keep semantics
	}

	prefix := attrs[attrPrefix]

	manifestsPrefix, ok := attrs[attrManifestsPrefix]
	if !ok || manifestsPrefix == "" {
		manifestsPrefix = "manifests/"
	}

	blobsPrefix, ok := attrs[attrBlobsPrefix]
	if !ok || blobsPrefix == "" {
		blobsPrefix = "blobs/"
	}

	names := []string{"buildkit"}
	if name, ok := attrs[attrName]; ok && name != "" {
		splitted := strings.Split(name, ";")
		if len(splitted) > 0 {
			names = splitted
		}
	}

	touchRefresh := 24 * time.Hour
	if v, ok := attrs[attrTouchRefresh]; ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			touchRefresh = d
		}
	}

	endpointURL := attrs[attrEndpointURL]
	accessKeyID := attrs[attrAccessKeyID]
	secretAccessKey := attrs[attrSecretAccessKey]
	sessionToken := attrs[attrSessionToken]

	usePathStyle := false
	if v, ok := attrs[attrUsePathStyle]; ok && v != "" {
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			usePathStyle = parsed
		}
	}

	uploadParallelism := 4
	if v, ok := attrs[attrUploadParallelism]; ok && v != "" {
		iv, err := strconv.Atoi(v)
		if err != nil || iv <= 0 {
			return Config{}, errors.Errorf("upload_parallelism must be a positive integer")
		}
		uploadParallelism = iv
	}

	return Config{
		Bucket:            bucket,
		Region:            region,
		Prefix:            prefix,
		ManifestsPrefix:   manifestsPrefix,
		BlobsPrefix:       blobsPrefix,
		Names:             names,
		TouchRefresh:      touchRefresh,
		EndpointURL:       endpointURL,
		AccessKeyID:       accessKeyID,
		SecretAccessKey:   secretAccessKey,
		SessionToken:      sessionToken,
		UsePathStyle:      usePathStyle,
		UploadParallelism: uploadParallelism,
	}, nil
}

func ResolveCacheExporterFunc() remotecache.ResolveCacheExporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Exporter, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, err
		}

		minioClient, err := newMinioClient(config)
		if err != nil {
			return nil, err
		}

		cacheChains := v1.NewCacheChains()
		return &exporter{CacheExporterTarget: cacheChains, chains: cacheChains, minioClient: minioClient, config: config}, nil
	}
}

type exporter struct {
	solver.CacheExporterTarget
	chains      *v1.CacheChains
	minioClient *minioClient
	config      Config
}

func (*exporter) Name() string { return "exporting cache to S3" }

func (e *exporter) Config() remotecache.Config {
	return remotecache.Config{Compression: compression.New(compression.Default)}
}

type nopCloserSectionReader struct{ *io.SectionReader }

func (*nopCloserSectionReader) Close() error { return nil }

func (e *exporter) Finalize(ctx context.Context) (map[string]string, error) {
	cacheConfig, descriptors, err := e.chains.Marshal(ctx)
	if err != nil {
		return nil, err
	}

	errorGroup, groupContext := errgroup.WithContext(ctx)
	tasks := make(chan int, e.config.UploadParallelism)

	go func() {
		for i := range cacheConfig.Layers {
			tasks <- i
		}
		close(tasks)
	}()

	for workerIndex := 0; workerIndex < e.config.UploadParallelism; workerIndex++ {
		errorGroup.Go(func() error {
			for index := range tasks {
				blob := cacheConfig.Layers[index].Blob
				descriptorProviderPair, ok := descriptors[blob]
				if !ok {
					return errors.Errorf("missing blob %s", blob)
				}
				if descriptorProviderPair.Descriptor.Annotations == nil {
					return errors.Errorf("invalid descriptor without annotations")
				}
				uncompressedAnnotation, ok := descriptorProviderPair.Descriptor.Annotations[labels.LabelUncompressed]
				if !ok {
					return errors.Errorf("invalid descriptor without uncompressed annotation")
				}
				diffID, err := digest.Parse(uncompressedAnnotation)
				if err != nil {
					return errors.Wrapf(err, "failed to parse uncompressed annotation")
				}

				key := e.minioClient.blobKey(descriptorProviderPair.Descriptor.Digest)
				lastMod, err := e.minioClient.exists(groupContext, key)
				if err != nil {
					return errors.Wrapf(err, "failed to check file presence in cache")
				}
				if lastMod != nil {
					if time.Since(*lastMod) > e.config.TouchRefresh {
						if err := e.minioClient.touch(groupContext, key); err != nil {
							return errors.Wrapf(err, "failed to touch file")
						}
					}
				} else {
					layerDone := progress.OneOff(groupContext, fmt.Sprintf("writing layer %s", blob))
					readerAt, err := descriptorProviderPair.Provider.ReaderAt(groupContext, descriptorProviderPair.Descriptor)
					if err != nil {
						return layerDone(errors.Wrap(err, "error reading layer blob from provider"))
					}
					defer readerAt.Close()

					section := &nopCloserSectionReader{io.NewSectionReader(readerAt, 0, readerAt.Size())}
					if err := e.minioClient.saveMutableAt(groupContext, key, section, readerAt.Size()); err != nil {
						return layerDone(errors.Wrap(err, "error writing layer blob"))
					}
					layerDone(nil)
				}

				layerAnnotations := &cacheimporttypes.LayerAnnotations{
					DiffID:    diffID,
					Size:      descriptorProviderPair.Descriptor.Size,
					MediaType: descriptorProviderPair.Descriptor.MediaType,
				}
				if createdAt, ok := descriptorProviderPair.Descriptor.Annotations["buildkit/createdat"]; ok {
					var createdAtTime time.Time
					if err := (&createdAtTime).UnmarshalText([]byte(createdAt)); err != nil {
						return err
					}
					layerAnnotations.CreatedAt = createdAtTime.UTC()
				}
				cacheConfig.Layers[index].Annotations = layerAnnotations
			}
			return nil
		})
	}

	if err := errorGroup.Wait(); err != nil {
		return nil, err
	}

	manifestData, err := json.Marshal(cacheConfig)
	if err != nil {
		return nil, err
	}

	for _, name := range e.config.Names {
		if err := e.minioClient.saveMutableAt(ctx, e.minioClient.manifestKey(name), bytes.NewReader(manifestData), int64(len(manifestData))); err != nil {
			return nil, errors.Wrapf(err, "error writing manifest: %s", name)
		}
	}
	return nil, nil
}

func ResolveCacheImporterFunc() remotecache.ResolveCacheImporterFunc {
	return func(ctx context.Context, _ session.Group, attrs map[string]string) (remotecache.Importer, ocispecs.Descriptor, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		minioClient, err := newMinioClient(config)
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		return &importer{minioClient: minioClient, config: config}, ocispecs.Descriptor{}, nil
	}
}

type importer struct {
	minioClient *minioClient
	config      Config
}

func (i *importer) makeDescriptorProviderPair(layer cacheimporttypes.CacheLayer) (*v1.DescriptorProviderPair, error) {
	if layer.Annotations == nil {
		return nil, errors.Errorf("cache layer with missing annotations")
	}
	if layer.Annotations.DiffID == "" {
		return nil, errors.Errorf("cache layer with missing diffid")
	}
	annotations := map[string]string{labels.LabelUncompressed: layer.Annotations.DiffID.String()}
	if !layer.Annotations.CreatedAt.IsZero() {
		if createdAtText, err := layer.Annotations.CreatedAt.MarshalText(); err == nil {
			annotations["buildkit/createdat"] = string(createdAtText)
		} else {
			return nil, err
		}
	}
	return &v1.DescriptorProviderPair{
		Provider: i.minioClient,
		Descriptor: ocispecs.Descriptor{
			MediaType:   layer.Annotations.MediaType,
			Digest:      layer.Blob,
			Size:        layer.Annotations.Size,
			Annotations: annotations,
		},
	}, nil
}

func (i *importer) load(ctx context.Context) (*v1.CacheChains, error) {
	var config cacheimporttypes.CacheConfig
	found, err := i.minioClient.getManifest(ctx, i.minioClient.manifestKey(i.config.Names[0]), &config)
	if err != nil {
		return nil, err
	}
	if !found {
		return v1.NewCacheChains(), nil
	}

	allLayers := v1.DescriptorProvider{}
	for _, layer := range config.Layers {
		descriptorProviderPair, err := i.makeDescriptorProviderPair(layer)
		if err != nil {
			return nil, err
		}
		allLayers[layer.Blob] = *descriptorProviderPair
	}
	cacheChains := v1.NewCacheChains()
	if err := v1.ParseConfig(config, allLayers, cacheChains); err != nil {
		return nil, err
	}
	return cacheChains, nil
}

func (i *importer) Resolve(ctx context.Context, _ ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error) {
	cacheChains, err := i.load(ctx)
	if err != nil {
		return nil, err
	}
	keysStorage, resultStorage, err := v1.NewCacheKeyStorage(cacheChains, w)
	if err != nil {
		return nil, err
	}
	return solver.NewCacheManager(ctx, id, keysStorage, resultStorage), nil
}

type readerAt struct {
	ReaderAtCloser
	size int64
}

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	return r.ReaderAtCloser.ReadAt(p, off)
}

func (r *readerAt) Size() int64 { return r.size }

type minioClient struct {
	client          *minio.Client
	bucket          string
	prefix          string
	blobsPrefix     string
	manifestsPrefix string
}

func newMinioClient(config Config) (*minioClient, error) {
	if config.EndpointURL == "" {
		config.EndpointURL = "https://s3.amazonaws.com"
	}

	parsedURL, err := url.Parse(config.EndpointURL)
	if err != nil {
		return nil, errors.Wrap(err, "invalid endpoint_url")
	}

	bucketLookup := minio.BucketLookupDNS
	if config.UsePathStyle {
		bucketLookup = minio.BucketLookupPath
	}

	client, err := minio.New(parsedURL.Host, &minio.Options{
		Creds: credentials.NewChainCredentials([]credentials.Provider{
			&credentials.Static{
				Value: credentials.Value{
					AccessKeyID:     config.AccessKeyID,
					SecretAccessKey: config.SecretAccessKey,
					SessionToken:    config.SessionToken,
					SignerType:      credentials.SignatureV4,
				},
			},
			&credentials.EnvAWS{},
			&credentials.EnvMinio{},
			&credentials.FileAWSCredentials{},
			&credentials.IAM{},
		}),
		Secure:       parsedURL.Scheme == "https",
		Region:       config.Region,
		BucketLookup: bucketLookup,
	})
	if err != nil {
		return nil, errors.Wrap(err, "create minio client")
	}

	return &minioClient{
		client:          client,
		bucket:          config.Bucket,
		prefix:          config.Prefix,
		blobsPrefix:     config.BlobsPrefix,
		manifestsPrefix: config.ManifestsPrefix,
	}, nil
}

func (m *minioClient) getManifest(ctx context.Context, key string, config *cacheimporttypes.CacheConfig) (bool, error) {
	object, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	defer object.Close()
	decoder := json.NewDecoder(object)
	if err := decoder.Decode(config); err != nil {
		return false, errors.WithStack(err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return false, errors.Errorf("unexpected data after JSON object")
	}
	return true, nil
}

func (m *minioClient) getReader(ctx context.Context, key string, offset int64) (io.ReadCloser, error) {
	getOptions := minio.GetObjectOptions{}
	if offset > 0 {
		if err := getOptions.SetRange(offset, 0); err != nil {
			return nil, err
		}
	}
	object, err := m.client.GetObject(ctx, m.bucket, key, getOptions)
	if err != nil {
		return nil, err
	}
	return object, nil
}

func (m *minioClient) saveMutableAt(ctx context.Context, key string, body io.Reader, size int64) error {
	_, err := m.client.PutObject(ctx, m.bucket, key, body, size, minio.PutObjectOptions{})
	return err
}

func (m *minioClient) exists(ctx context.Context, key string) (*time.Time, error) {
	stat, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	lastModified := stat.LastModified
	return &lastModified, nil
}

func (m *minioClient) touch(ctx context.Context, key string) error {
	// Minio will internally perform multipart copy when supported
	copySourceOptions := minio.CopySrcOptions{Bucket: m.bucket, Object: key}
	copyDestinationOptions := minio.CopyDestOptions{
		Bucket:          m.bucket,
		Object:          key,
		ReplaceMetadata: true,
		UserMetadata:    map[string]string{"updated-at": time.Now().UTC().Format(time.RFC3339Nano)},
	}
	_, err := m.client.CopyObject(ctx, copyDestinationOptions, copySourceOptions)
	return err
}

func (m *minioClient) ReaderAt(ctx context.Context, descriptor ocispecs.Descriptor) (content.ReaderAt, error) {
	readerAtCloser := toReaderAtCloser(func(offset int64) (io.ReadCloser, error) {
		return m.getReader(ctx, m.blobKey(descriptor.Digest), offset)
	})
	return &readerAt{ReaderAtCloser: readerAtCloser, size: descriptor.Size}, nil
}

func (m *minioClient) manifestKey(name string) string { return m.prefix + m.manifestsPrefix + name }

func (m *minioClient) blobKey(digestValue digest.Digest) string {
	return m.prefix + m.blobsPrefix + digestValue.String()
}

func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	if resp.StatusCode == http.StatusNotFound {
		return true
	}
	switch strings.ToLower(resp.Code) {
	case "nosuchkey", "notfound", "no such key":
		return true
	}
	return false
}
