package azblob

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/pkg/errors"
)

const (
	attrSecretAccessKey = "secret_access_key"
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
			return &Config{}, fmt.Errorf("either ${BUILDKIT_AZURE_STORAGE_ACCOUNT_URL} or account_url attribute is required for azblob cache")
		}
	}

	accountURL, err := url.Parse(accountURLString)
	if err != nil {
		return &Config{}, fmt.Errorf("azure storage account url provided is not a valid url: %v", err)
	}

	accountName := strings.Split(accountURL.Hostname(), ".")[0]

	container, ok := attrs[attrContainer]
	if !ok {
		container, ok = os.LookupEnv("BUILDKIT_AZURE_STORAGE_CONTAINER")
		if !ok {
			container = "buildkit-cache"
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
		Container:       container,
		Prefix:          prefix,
		Names:           names,
		ManifestsPrefix: manifestsPrefix,
		BlobsPrefix:     blobsPrefix,
		secretAccessKey: secretAccessKey,
	}

	return &config, nil
}

func createContainerClient(ctx context.Context, config *Config) (*azblob.ContainerClient, error) {
	var serviceClient *azblob.ServiceClient
	if config.secretAccessKey != "" {
		sharedKey, err := azblob.NewSharedKeyCredential(config.AccountName, config.secretAccessKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create shared key: %v", err)
		}
		serviceClient, err = azblob.NewServiceClientWithSharedKey(config.AccountURL, sharedKey, &azblob.ClientOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to created service client from shared key: %v", err)
		}
	} else {
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create default azure credentials: %v", err)
		}

		serviceClient, err = azblob.NewServiceClient(config.AccountURL, cred, &azblob.ClientOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create service client: %v", err)
		}
	}

	ctx, cnclFn := context.WithTimeout(ctx, time.Second*60)
	defer cnclFn()

	containerClient, err := serviceClient.NewContainerClient(config.Container)
	if err != nil {
		return nil, errors.Wrap(err, "error creating container client")
	}

	_, err = containerClient.GetProperties(ctx, &azblob.ContainerGetPropertiesOptions{})
	if err == nil {
		return containerClient, nil
	}

	var se *azblob.StorageError
	if errors.As(err, &se) && se.ErrorCode == azblob.StorageErrorCodeContainerNotFound {
		ctx, cnclFn := context.WithTimeout(ctx, time.Minute*5)
		defer cnclFn()
		_, err := containerClient.Create(ctx, &azblob.ContainerCreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create cache container %s: %v", config.Container, err)
		}

		return containerClient, nil
	}

	return nil, fmt.Errorf("failed to get properties of cache container %s: %v", config.Container, err)
}

func manifestKey(config *Config, name string) string {
	key := filepath.Join(config.Prefix, config.ManifestsPrefix, name)
	return key
}

func blobKey(config *Config, digest string) string {
	key := filepath.Join(config.Prefix, config.BlobsPrefix, digest)
	return key
}

func blobExists(ctx context.Context, containerClient *azblob.ContainerClient, blobKey string) (bool, error) {
	blobClient, err := containerClient.NewBlobClient(blobKey)
	if err != nil {
		return false, errors.Wrap(err, "error creating blob client")
	}

	ctx, cnclFn := context.WithTimeout(ctx, time.Second*60)
	defer cnclFn()
	_, err = blobClient.GetProperties(ctx, &azblob.BlobGetPropertiesOptions{})
	if err == nil {
		return true, nil
	}

	var se *azblob.StorageError
	if errors.As(err, &se) && se.ErrorCode == azblob.StorageErrorCodeBlobNotFound {
		return false, nil
	}

	return false, fmt.Errorf("failed to check blob %s existence: %v", blobKey, err)
}
