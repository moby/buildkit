package azblob

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/pkg/errors"
)

const (
	attrSecretAccessKey = "secret_access_key"
	attrAccountName     = "account_name"
	attrAccountURL      = "account_url"
	attrPrefix          = "prefix"
	attrManifestsPrefix = "manifests_prefix"
	attrBlobsPrefix     = "blobs_prefix"
	attrName            = "name"
	attrContainer       = "container"
	IOConcurrency       = 4
	IOChunkSize         = 32 * 1024 * 1024
)

type Config struct {
	AccountURL      string
	Container       string
	Prefix          string
	ManifestsPrefix string
	BlobsPrefix     string
	Names           []string
	AccountName     string
	secretAccessKey string
}

func getConfig(attrs map[string]string) (*Config, error) {
	accountURLString, ok := attrs[attrAccountURL]
	if !ok {
		accountURLString, ok = os.LookupEnv("BUILDKIT_AZURE_STORAGE_ACCOUNT_URL")
		if !ok {
			return nil, errors.New("either ${BUILDKIT_AZURE_STORAGE_ACCOUNT_URL} or account_url attribute is required for azblob cache")
		}
	}

	accountURL, err := url.Parse(accountURLString)
	if err != nil {
		return nil, errors.Wrap(err, "azure storage account url provided is not a valid url")
	}

	accountName, ok := attrs[attrAccountName]
	if !ok {
		accountName, ok = os.LookupEnv("BUILDKIT_AZURE_STORAGE_ACCOUNT_NAME")
		if !ok {
			accountName = strings.Split(accountURL.Hostname(), ".")[0]
		}
	}
	if accountName == "" {
		return nil, errors.New("unable to retrieve account name from account url or ${BUILDKIT_AZURE_STORAGE_ACCOUNT_NAME} or account_name attribute for azblob cache")
	}

	ctn, ok := attrs[attrContainer]
	if !ok {
		ctn, ok = os.LookupEnv("BUILDKIT_AZURE_STORAGE_CONTAINER")
		if !ok {
			ctn = "buildkit-cache"
		}
	}

	prefix, ok := attrs[attrPrefix]
	if !ok {
		prefix, _ = os.LookupEnv("BUILDKIT_AZURE_STORAGE_PREFIX")
	}

	manifestsPrefix, ok := attrs[attrManifestsPrefix]
	if !ok {
		manifestsPrefix = "manifests"
	}

	blobsPrefix, ok := attrs[attrBlobsPrefix]
	if !ok {
		blobsPrefix = "blobs"
	}

	names := []string{"buildkit"}
	name, ok := attrs[attrName]
	if ok {
		splittedNames := strings.Split(name, ";")
		if len(splittedNames) > 0 {
			names = splittedNames
		}
	}

	secretAccessKey := attrs[attrSecretAccessKey]

	config := Config{
		AccountURL:      accountURLString,
		AccountName:     accountName,
		Container:       ctn,
		Prefix:          prefix,
		Names:           names,
		ManifestsPrefix: manifestsPrefix,
		BlobsPrefix:     blobsPrefix,
		secretAccessKey: secretAccessKey,
	}

	return &config, nil
}

type Client struct {
	azblob.Client
	config    *Config
	sharedKey *azblob.SharedKeyCredential
	cred      *azidentity.DefaultAzureCredential
}

func newClient(ctx context.Context, config *Config) (*Client, error) {
	var client *azblob.Client
	var sharedKey *azblob.SharedKeyCredential
	var cred *azidentity.DefaultAzureCredential
	var err error
	if config.secretAccessKey != "" {
		sharedKey, err = azblob.NewSharedKeyCredential(config.AccountName, config.secretAccessKey)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create shared key")
		}
		client, err = azblob.NewClientWithSharedKeyCredential(config.AccountURL, sharedKey, &azblob.ClientOptions{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to created client from shared key")
		}
	} else {
		cred, err = azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create default azure credentials")
		}
		client, err = azblob.NewClient(config.AccountURL, cred, &azblob.ClientOptions{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to create client")
		}
	}

	ctx, cnclFn := context.WithCancelCause(ctx)
	ctx, _ = context.WithTimeoutCause(ctx, time.Second*60, errors.WithStack(context.DeadlineExceeded))
	defer cnclFn(errors.WithStack(context.Canceled))

	ctnURL := runtime.JoinPaths(client.URL(), config.Container)
	var cc *container.Client
	if sharedKey != nil {
		cc, err = container.NewClientWithSharedKeyCredential(ctnURL, sharedKey, nil)
	} else {
		cc, err = container.NewClient(ctnURL, cred, nil)
	}
	if err != nil {
		return nil, errors.Wrap(err, "failed to create container client")
	}

	_, err = cc.GetProperties(ctx, &container.GetPropertiesOptions{})
	if err != nil {
		if bloberror.HasCode(err, bloberror.ContainerNotFound) {
			_, err = client.CreateContainer(ctx, config.Container, nil)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to create cache container %s", config.Container)
			}
		}
		return nil, errors.Wrapf(err, "failed to get properties of container %s", config.Container)
	}

	return &Client{
		Client:    *client,
		config:    config,
		sharedKey: sharedKey,
		cred:      cred,
	}, nil
}

func (c *Client) manifestKey(name string) string {
	key := filepath.Join(c.config.Prefix, c.config.ManifestsPrefix, name)
	return key
}

func (c *Client) blobKey(digest string) string {
	key := filepath.Join(c.config.Prefix, c.config.BlobsPrefix, digest)
	return key
}

func (c *Client) blobExists(ctx context.Context, blobName string) (bool, error) {
	blobURL := runtime.JoinPaths(c.URL(), c.config.Container, url.PathEscape(blobName))

	var bc *blob.Client
	var err error
	if c.sharedKey != nil {
		bc, err = blob.NewClientWithSharedKeyCredential(blobURL, c.sharedKey, nil)
	} else {
		bc, err = blob.NewClient(blobURL, c.cred, nil)
	}
	if err != nil {
		return false, errors.Wrap(err, "failed to create blob client")
	}

	ctx, cnclFn := context.WithCancelCause(ctx)
	ctx, _ = context.WithTimeoutCause(ctx, time.Second*60, errors.WithStack(context.DeadlineExceeded))
	defer cnclFn(errors.WithStack(context.Canceled))

	_, err = bc.GetProperties(ctx, &blob.GetPropertiesOptions{})
	if err == nil {
		return true, nil
	} else if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return false, nil
	}
	return false, errors.Wrapf(err, "failed to check blob %s existence", blobName)
}
