package gcs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"

	"cloud.google.com/go/storage"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
	sts "google.golang.org/api/sts/v1"
)

const (
	attrBucket                  = "bucket"
	attrPrefix                  = "prefix"
	attrManifestsPrefix         = "manifests_prefix"
	attrBlobsPrefix             = "blobs_prefix"
	attrName                    = "name"
	attrTouchRefresh            = "touch_refresh"
	attrEndpointURL             = "endpoint_url"
	attrUsePathStyle            = "use_path_style"
	attrGcpJSONKey              = "gcp_json_key"
	attrOIDCTokenID             = "oidc_token_id"
	attrOIDCProjectID           = "oidc_project_id"
	attrOIDCPoolID              = "oidc_pool_id"
	attrOIDCProviderID          = "oidc_provider_id"
	attrOIDCServiceAccountEmail = "oidc_service_account_email"

	audienceFormat = "//iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s"
	scopeURL       = "https://www.googleapis.com/auth/cloud-platform"
)

type gcsClient struct {
	client          *storage.Client
	bucket          string
	prefix          string
	blobsPrefix     string
	manifestsPrefix string
}

type Config struct {
	Bucket                  string
	Region                  string
	Prefix                  string
	ManifestsPrefix         string
	BlobsPrefix             string
	Names                   []string
	TouchRefresh            time.Duration
	EndpointURL             string
	UsePathStyle            bool
	AccountID               string
	Token                   string
	OIDCTokenID             string
	GcpJSONKey              string
	OIDCProjectID           string
	OIDCPoolID              string
	OIDCProviderID          string
	OIDCServiceAccountEmail string
}

func getConfig(attrs map[string]string) (Config, error) {
	bucket, ok := attrs[attrBucket]
	if !ok {
		return Config{}, errors.Errorf("Bucket not set for GCS Cache")
	}

	prefix := attrs[attrPrefix]

	manifestsPrefix, ok := attrs[attrManifestsPrefix]
	if !ok {
		manifestsPrefix = "manifests/"
	}

	blobsPrefix, ok := attrs[attrBlobsPrefix]
	if !ok {
		blobsPrefix = "blobs/"
	}

	names := []string{"buildkit"}
	name, ok := attrs[attrName]
	if ok {
		splittedNames := strings.Split(name, ";")
		if len(splittedNames) > 0 {
			names = splittedNames
		}
	}

	touchRefresh := 24 * time.Hour

	touchRefreshStr, ok := attrs[attrTouchRefresh]
	if ok {
		touchRefreshFromUser, err := time.ParseDuration(touchRefreshStr)
		if err == nil {
			touchRefresh = touchRefreshFromUser
		}
	}

	endpointURL := attrs[attrEndpointURL]

	usePathStyle := false
	usePathStyleStr, ok := attrs[attrUsePathStyle]
	if ok {
		usePathStyleUser, err := strconv.ParseBool(usePathStyleStr)
		if err == nil {
			usePathStyle = usePathStyleUser
		}
	}

	gcpJSONKey := attrs[attrGcpJSONKey]
	oidcTokenID := attrs[attrOIDCTokenID]
	oidcProjectID := attrs[attrOIDCProjectID]
	oidcProviderID := attrs[attrOIDCProviderID]
	oidcPoolID := attrs[attrOIDCPoolID]
	oidcServiceAccountEmail := attrs[attrOIDCServiceAccountEmail]

	return Config{
		Bucket:                  bucket,
		Prefix:                  prefix,
		ManifestsPrefix:         manifestsPrefix,
		BlobsPrefix:             blobsPrefix,
		Names:                   names,
		TouchRefresh:            touchRefresh,
		EndpointURL:             endpointURL,
		UsePathStyle:            usePathStyle,
		GcpJSONKey:              gcpJSONKey,
		OIDCTokenID:             oidcTokenID,
		OIDCProjectID:           oidcProjectID,
		OIDCProviderID:          oidcProviderID,
		OIDCPoolID:              oidcPoolID,
		OIDCServiceAccountEmail: oidcServiceAccountEmail,
	}, nil
}

func newGCSClient(c Config) (*gcsClient, error) {
	ctx := context.Background()
	var opts []option.ClientOption

	// Try using GCP JSON Key for authentication
	if c.GcpJSONKey != "" {
		decodedKey, err := base64.StdEncoding.DecodeString(c.GcpJSONKey)
		if err != nil {
			return nil, errors.Errorf("Unable to decode base64 GCP JSON Key, %v", err)
		}

		creds, err := google.CredentialsFromJSON(ctx, decodedKey, storage.ScopeFullControl)
		if err != nil {
			return nil, errors.Errorf("Unable to load GCP JSON Key, %v", err)
		}
		opts = append(opts, option.WithCredentials(creds))
	} else if c.OIDCTokenID != "" && c.OIDCProjectID != "" && c.OIDCPoolID != "" && c.OIDCProviderID != "" && c.OIDCServiceAccountEmail != "" {
		// Use OIDC-based authentication with token refresh
		tokenSource := &oidcTokenSource{
			ctx:                     ctx,
			oidcTokenID:             c.OIDCTokenID,
			oidcProjectID:           c.OIDCProjectID,
			oidcPoolID:              c.OIDCPoolID,
			oidcProviderID:          c.OIDCProviderID,
			oidcServiceAccountEmail: c.OIDCServiceAccountEmail,
		}
		opts = append(opts, option.WithTokenSource(tokenSource))
	} else {
		creds, err := google.FindDefaultCredentials(ctx, storage.ScopeFullControl)
		if err != nil {
			return nil, errors.New("error finding default credentials for gcs")
		}
		opts = append(opts, option.WithCredentials(creds))
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &gcsClient{
		client:          client,
		bucket:          c.Bucket,
		prefix:          c.Prefix,
		blobsPrefix:     c.BlobsPrefix,
		manifestsPrefix: c.ManifestsPrefix,
	}, nil
}

type oidcTokenSource struct {
	ctx                     context.Context
	oidcTokenID             string
	oidcProjectID           string
	oidcPoolID              string
	oidcProviderID          string
	oidcServiceAccountEmail string
}

func (ts *oidcTokenSource) Token() (*oauth2.Token, error) {
	// Retrieve the federated token
	federatedToken, err := GetFederalToken(ts.oidcTokenID, ts.oidcProjectID, ts.oidcPoolID, ts.oidcProviderID)
	if err != nil {
		return nil, errors.Errorf("OIDC token retrieval failed: %v", err)
	}

	// Get Google Cloud access token using the federated token
	accessToken, err := GetGoogleCloudAccessToken(federatedToken, ts.oidcServiceAccountEmail)
	if err != nil {
		return nil, errors.Errorf("Error getting Google Cloud Access Token: %v", err)
	}

	// You could include the expiry time if you are able to get it from `GetGoogleCloudAccessToken`
	return &oauth2.Token{
		AccessToken: accessToken,
	}, nil
}

func GetFederalToken(idToken, projectID, poolID, providerID string) (string, error) {
	ctx := context.Background()
	stsService, err := sts.NewService(ctx, option.WithoutAuthentication())
	if err != nil {
		return "", err
	}

	audience := fmt.Sprintf(audienceFormat, projectID, poolID, providerID)

	tokenRequest := &sts.GoogleIdentityStsV1ExchangeTokenRequest{
		GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
		SubjectToken:       idToken,
		Audience:           audience,
		Scope:              scopeURL,
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:id_token",
	}

	tokenResponse, err := stsService.V1.Token(tokenRequest).Do()
	if err != nil {
		return "", err
	}

	return tokenResponse.AccessToken, nil
}

func GetGoogleCloudAccessToken(federatedToken string, serviceAccountEmail string) (string, error) {
	ctx := context.Background()
	token := &oauth2.Token{AccessToken: federatedToken}
	service, err := iamcredentials.NewService(ctx, option.WithTokenSource(oauth2.StaticTokenSource(token)))
	if err != nil {
		return "", err
	}

	name := "projects/-/serviceAccounts/" + serviceAccountEmail
	// rb (request body) specifies parameters for generating an access token.
	rb := &iamcredentials.GenerateAccessTokenRequest{
		Scope: []string{scopeURL},
	}
	// Generate an access token for the service account using the specified parameters
	resp, err := service.Projects.ServiceAccounts.GenerateAccessToken(name, rb).Do()
	if err != nil {
		return "", err
	}

	return resp.AccessToken, nil
}

// ResolveCacheExporterFunc for s3 cache exporter.
func ResolveCacheExporterFunc() remotecache.ResolveCacheExporterFunc {
	return func(ctx context.Context, g session.Group, attrs map[string]string) (remotecache.Exporter, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, err
		}

		gcsClient, err := newGCSClient(config)
		if err != nil {
			return nil, err
		}
		cc := v1.NewCacheChains()
		return &exporter{CacheExporterTarget: cc, chains: cc, gcsClient: gcsClient, config: config}, nil
	}
}

type exporter struct {
	solver.CacheExporterTarget
	chains    *v1.CacheChains
	gcsClient *gcsClient
	config    Config
}

func (*exporter) Name() string {
	return "exporting cache to Google Cloud Storage"
}

func (e *exporter) Config() remotecache.Config {
	return remotecache.Config{
		Compression: compression.New(compression.Default),
	}
}

type nopCloserSectionReader struct {
	*io.SectionReader
}

func (*nopCloserSectionReader) Close() error { return nil }

func (e *exporter) Finalize(ctx context.Context) (map[string]string, error) {
	cacheConfig, descs, err := e.chains.Marshal(ctx)
	if err != nil {
		return nil, err
	}

	for i, l := range cacheConfig.Layers {
		dgstPair, ok := descs[l.Blob]
		if !ok {
			return nil, errors.Errorf("missing blob %s", l.Blob)
		}
		if dgstPair.Descriptor.Annotations == nil {
			return nil, errors.Errorf("invalid descriptor without annotations")
		}
		v, ok := dgstPair.Descriptor.Annotations["containerd.io/uncompressed"]
		if !ok {
			return nil, errors.Errorf("invalid descriptor without uncompressed annotation")
		}
		diffID, err := digest.Parse(v)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse uncompressed annotation")
		}

		key := e.gcsClient.blobKey(dgstPair.Descriptor.Digest)
		exists, err := e.gcsClient.exists(ctx, key)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to check file presence in cache")
		}
		if exists != nil {
			if time.Since(*exists) > e.config.TouchRefresh {
				err = e.gcsClient.touch(ctx, key)
				if err != nil {
					return nil, errors.Wrapf(err, "failed to touch file")
				}
			}
		} else {
			layerDone := progress.OneOff(ctx, fmt.Sprintf("writing layer %s", l.Blob))
			dt, err := content.ReadBlob(ctx, dgstPair.Provider, dgstPair.Descriptor)
			if err != nil {
				return nil, layerDone(err)
			}
			if err := e.gcsClient.saveMutable(ctx, key, dt); err != nil {
				return nil, layerDone(errors.Wrap(err, "error writing layer blob"))
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
		cacheConfig.Layers[i].Annotations = la
	}

	dt, err := json.Marshal(cacheConfig)
	if err != nil {
		return nil, err
	}

	for _, name := range e.config.Names {
		if err := e.gcsClient.saveMutable(ctx, e.gcsClient.manifestKey(name), dt); err != nil {
			return nil, errors.Wrapf(err, "error writing manifest: %s", name)
		}
	}
	return nil, nil
}

// ResolveCacheImporterFunc for s3 cache importer.
func ResolveCacheImporterFunc() remotecache.ResolveCacheImporterFunc {
	return func(ctx context.Context, _ session.Group, attrs map[string]string) (remotecache.Importer, ocispecs.Descriptor, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		gcsClient, err := newGCSClient(config)
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		return &importer{gcsClient, config}, ocispecs.Descriptor{}, nil
	}
}

type importer struct {
	gcsClient *gcsClient
	config    Config
}

func (i *importer) makeDescriptorProviderPair(l v1.CacheLayer) (*v1.DescriptorProviderPair, error) {
	if l.Annotations == nil {
		return nil, errors.Errorf("cache layer with missing annotations")
	}
	if l.Annotations.DiffID == "" {
		return nil, errors.Errorf("cache layer with missing diffid")
	}
	annotations := map[string]string{}
	annotations[labels.LabelUncompressed] = l.Annotations.DiffID.String()
	if !l.Annotations.CreatedAt.IsZero() {
		txt, err := l.Annotations.CreatedAt.MarshalText()
		if err != nil {
			return nil, err
		}
		annotations["buildkit/createdat"] = string(txt)
	}
	return &v1.DescriptorProviderPair{
		Provider: i.gcsClient,
		Descriptor: ocispecs.Descriptor{
			MediaType:   l.Annotations.MediaType,
			Digest:      l.Blob,
			Size:        l.Annotations.Size,
			Annotations: annotations,
		},
	}, nil
}

func (i *importer) load(ctx context.Context) (*v1.CacheChains, error) {
	var config v1.CacheConfig
	found, err := i.gcsClient.getManifest(ctx, i.gcsClient.manifestKey(i.config.Names[0]), &config)
	if err != nil {
		return nil, err
	}
	if !found {
		return v1.NewCacheChains(), nil
	}

	allLayers := v1.DescriptorProvider{}

	for _, l := range config.Layers {
		dpp, err := i.makeDescriptorProviderPair(l)
		if err != nil {
			return nil, err
		}
		allLayers[l.Blob] = *dpp
	}

	cc := v1.NewCacheChains()
	if err := v1.ParseConfig(config, allLayers, cc); err != nil {
		return nil, err
	}
	return cc, nil
}

func (i *importer) Resolve(ctx context.Context, _ ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error) {
	cc, err := i.load(ctx)
	if err != nil {
		return nil, err
	}

	keysStorage, resultStorage, err := v1.NewCacheKeyStorage(cc, w)
	if err != nil {
		return nil, err
	}

	return solver.NewCacheManager(ctx, id, keysStorage, resultStorage), nil
}

type readerAt struct {
	ReaderAtCloser
	size int64
}

func (r *readerAt) Size() int64 {
	return r.size
}

func (gcsClient *gcsClient) getManifest(ctx context.Context, key string, config *v1.CacheConfig) (bool, error) {
	reader, err := gcsClient.getReader(ctx, key)
	if err != nil {
		return false, err
	}
	defer reader.Close()

	if err := json.NewDecoder(reader).Decode(config); err != nil {
		return false, errors.WithStack(err)
	}
	return true, nil
}

func (gcsClient *gcsClient) getReader(ctx context.Context, key string) (io.ReadCloser, error) {
	obj := gcsClient.client.Bucket(gcsClient.bucket).Object(key)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

func (gcsClient *gcsClient) saveMutable(ctx context.Context, key string, value []byte) error {
	obj := gcsClient.client.Bucket(gcsClient.bucket).Object(key)
	writer := obj.NewWriter(ctx)
	defer writer.Close()

	if _, err := writer.Write(value); err != nil {
		return err
	}
	return nil
}

func (gcsClient *gcsClient) exists(ctx context.Context, key string) (*time.Time, error) {
	obj := gcsClient.client.Bucket(gcsClient.bucket).Object(key)
	_, err := obj.Attrs(ctx)
	if err != nil {
		if err == storage.ErrObjectNotExist {
			return nil, nil
		}
		return nil, err
	}
	return nil, nil
}

func (gcsClient *gcsClient) touch(ctx context.Context, key string) error {
	obj := gcsClient.client.Bucket(gcsClient.bucket).Object(key)
	currentAttrs, err := obj.Attrs(ctx)
	if err != nil {
		return err
	}

	// Update metadata to "touch" the object
	metadata := currentAttrs.Metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata["updated-at"] = time.Now().Format(time.RFC3339)

	objAttrsToUpdate := storage.ObjectAttrsToUpdate{
		Metadata: metadata,
	}

	_, err = obj.Update(ctx, objAttrsToUpdate)
	return err
}

func (gcsClient *gcsClient) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	readerAtCloser := toReaderAtCloser(func(offset int64) (io.ReadCloser, error) {
		return gcsClient.getReader(ctx, gcsClient.blobKey(desc.Digest))
	})
	return &readerAt{ReaderAtCloser: readerAtCloser, size: desc.Size}, nil
}

func (gcsClient *gcsClient) manifestKey(name string) string {
	return gcsClient.prefix + gcsClient.manifestsPrefix + name
}

func (gcsClient *gcsClient) blobKey(dgst digest.Digest) string {
	return gcsClient.prefix + gcsClient.blobsPrefix + dgst.String()
}
