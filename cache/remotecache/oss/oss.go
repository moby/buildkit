package oss

import (
	"os"
	"strings"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const (
	attrBucket          = "bucket"
	attrPrefix          = "prefix"
	attrManifestsPrefix = "manifests_prefix"
	attrBlobsPrefix     = "blobs_prefix"
	attrName            = "name"
	attrEndpointURL     = "endpoint_url"
	attrAccessKeyID     = "access_key_id"
	attrSecretAccessKey = "secret_access_key"
	attrSecurityToken   = "security_token"
)

type Config struct {
	Bucket          string
	Prefix          string
	ManifestsPrefix string
	BlobsPrefix     string
	Names           []string
	EndpointURL     string
	AccessKeyID     string
	SecretAccessKey string
	SecurityToken   string
}

func getConfig(attrs map[string]string) (*Config, error) {
	bucket, ok := attrs[attrBucket]
	if !ok {
		bucket, ok = os.LookupEnv("ALIBABA_CLOUD_OSS_BUCKET")
		if !ok {
			return &Config{}, errors.Errorf("bucket ($ALIBABA_CLOUD_OSS_BUCKET) not set for oss cache")
		}
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

	endpointURL, ok := attrs[attrEndpointURL]
	if !ok {
		return &Config{}, errors.Errorf("endpoint_url not set for oss cache")
	}

	accessKeyID := attrs[attrAccessKeyID]
	secretAccessKey := attrs[attrSecretAccessKey]
	securityToken := attrs[attrSecurityToken]

	return &Config{
		Bucket:          bucket,
		Prefix:          prefix,
		ManifestsPrefix: manifestsPrefix,
		BlobsPrefix:     blobsPrefix,
		Names:           names,
		EndpointURL:     endpointURL,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		SecurityToken:   securityToken,
	}, nil
}

type ossClient struct {
	*oss.Client
	*oss.Bucket
	prefix          string
	blobsPrefix     string
	manifestsPrefix string
}

func createOSSClient(config *Config) (*ossClient, error) {
	var client *oss.Client
	var err error
	if config.AccessKeyID != "" && config.SecretAccessKey != "" {
		client, err = oss.New(config.EndpointURL, config.AccessKeyID, config.SecretAccessKey, oss.SecurityToken(config.SecurityToken))
		if err != nil {
			return nil, errors.Wrap(err, "failed to create oss client")
		}
	} else {
		provider, err := oss.NewEnvironmentVariableCredentialsProvider()
		if err != nil {
			return nil, errors.Wrap(err, "failed to new environment variable credentials provider")
		}
		client, err = oss.New(config.EndpointURL, "", "", oss.SetCredentialsProvider(&provider))
		if err != nil {
			return nil, errors.Wrap(err, "failed to create oss client with environment variable credentials provider")
		}
	}
	bucket, err := client.Bucket(config.Bucket)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get bucket")
	}
	return &ossClient{
		Client:          client,
		Bucket:          bucket,
		prefix:          config.Prefix,
		blobsPrefix:     config.BlobsPrefix,
		manifestsPrefix: config.ManifestsPrefix,
	}, nil
}

func blobExists(ossClient *ossClient, blobKey string) (bool, error) {
	isExist, err := ossClient.Bucket.IsObjectExist(blobKey)
	if err != nil {
		return false, errors.Wrapf(err, "failed to upload blob %s: %v", blobKey, err)
	}
	return isExist, nil
}

func (ossClient *ossClient) manifestKey(name string) string {
	return ossClient.prefix + ossClient.manifestsPrefix + name
}

func (ossClient *ossClient) blobKey(dgst digest.Digest) string {
	return ossClient.prefix + ossClient.blobsPrefix + dgst.String()
}
