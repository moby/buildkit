package azblob

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	digest "github.com/opencontainers/go-digest"
	pkgerrors "github.com/pkg/errors"
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
			return &Config{}, errors.New("either ${BUILDKIT_AZURE_STORAGE_ACCOUNT_URL} or account_url attribute is required for azblob cache")
		}
	}

	accountURL, err := url.Parse(accountURLString)
	if err != nil {
		return &Config{}, fmt.Errorf("azure storage account url provided is not a valid url"+": %w", err)
	}

	accountName, ok := attrs[attrAccountName]
	if !ok {
		accountName, ok = os.LookupEnv("BUILDKIT_AZURE_STORAGE_ACCOUNT_NAME")
		if !ok {
			accountName = strings.Split(accountURL.Hostname(), ".")[0]
		}
	}
	if accountName == "" {
		return &Config{}, errors.New("unable to retrieve account name from account url or ${BUILDKIT_AZURE_STORAGE_ACCOUNT_NAME} or account_name attribute for azblob cache")
	}

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

func createContainerClient(ctx context.Context, config *Config) (*container.Client, error) {
	var client *azblob.Client
	if config.secretAccessKey != "" {
		sharedKey, err := azblob.NewSharedKeyCredential(config.AccountName, config.secretAccessKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create shared key"+": %w", err)
		}
		client, err = azblob.NewClientWithSharedKeyCredential(config.AccountURL, sharedKey, &azblob.ClientOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to created service client from shared key"+": %w", err)
		}
	} else {
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create default azure credentials"+": %w", err)
		}

		client, err = azblob.NewClient(config.AccountURL, cred, &azblob.ClientOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create service client"+": %w", err)
		}
	}

	ctx, cnclFn := context.WithCancelCause(ctx)
	ctx, _ = context.WithTimeoutCause(ctx, time.Second*60, pkgerrors.WithStack(context.DeadlineExceeded)) //nolint:govet
	defer cnclFn(pkgerrors.WithStack(context.Canceled))

	containerClient := client.ServiceClient().NewContainerClient(config.Container)

	_, err := containerClient.GetProperties(ctx, &container.GetPropertiesOptions{})
	if err == nil {
		return containerClient, nil
	}

	if bloberror.HasCode(err, bloberror.ContainerNotFound) {
		ctx, cnclFn := context.WithCancelCause(ctx)
		ctx, _ = context.WithTimeoutCause(ctx, time.Minute*5, pkgerrors.WithStack(context.DeadlineExceeded)) //nolint:govet
		defer cnclFn(pkgerrors.WithStack(context.Canceled))
		_, err := containerClient.Create(ctx, &container.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create cache container %s: %w", config.Container, err)
		}
		return containerClient, nil
	}

	return nil, fmt.Errorf("failed to get properties of cache container %s: %w", config.Container, err)
}

func manifestKey(config *Config, name string) string {
	key := filepath.Join(config.Prefix, config.ManifestsPrefix, name)
	return key
}

func blobKey(config *Config, digest digest.Digest) string {
	key := filepath.Join(config.Prefix, config.BlobsPrefix, digest.String())
	return key
}

func blobExists(ctx context.Context, containerClient *container.Client, blobKey string) (bool, error) {
	blobClient := containerClient.NewBlobClient(blobKey)
	ctx, cnclFn := context.WithCancelCause(ctx)
	ctx, _ = context.WithTimeoutCause(ctx, time.Second*60, pkgerrors.WithStack(context.DeadlineExceeded)) //nolint:govet
	defer cnclFn(pkgerrors.WithStack(context.Canceled))
	_, err := blobClient.GetProperties(ctx, &blob.GetPropertiesOptions{})
	if err == nil {
		return true, nil
	}

	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return false, nil
	}

	return false, fmt.Errorf("failed to check blob %s existence: %w", blobKey, err)
}
