// Copyright 2020 Ant Group. All rights reserved.
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/containerd/containerd/archive/compression"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
)

const SupportedOS = "linux"
const SupportedArch = "amd64"

const defaultRetryAttempts = 3
const defaultRetryInterval = time.Second * 2

func WithRetry(op func() error) error {
	var err error
	attempts := defaultRetryAttempts
	for attempts > 0 {
		attempts--
		if err != nil {
			logrus.Warnf("Retry due to error: %s", err)
			time.Sleep(defaultRetryInterval)
		}
		if err = op(); err == nil {
			break
		}
	}
	return err
}

func MarshalToDesc(data interface{}, mediaType string) (*ocispec.Descriptor, []byte, error) {
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, nil, err
	}

	dataDigest := digest.FromBytes(bytes)
	desc := ocispec.Descriptor{
		Digest:    dataDigest,
		Size:      int64(len(bytes)),
		MediaType: mediaType,
	}

	return &desc, bytes, nil
}

func IsSupportedPlatform(os, arch string) bool {
	// Default we assume that empty OS/Arch should be
	// a supported platform likes linux/amd64
	if os == "" && arch == "" {
		logrus.Warnln("Found empty OS/Arch platform manifest")
		return true
	}
	if os == SupportedOS && arch == SupportedArch {
		return true
	}
	return false
}

func IsNydusPlatform(platform *ocispec.Platform) bool {
	if platform != nil && platform.OSFeatures != nil {
		for _, key := range platform.OSFeatures {
			if key == ManifestOSFeatureNydus {
				return true
			}
		}
	}
	return false
}

func UnpackFile(reader io.Reader, source, target string) error {
	rdr, err := compression.DecompressStream(reader)
	if err != nil {
		return err
	}
	defer rdr.Close()

	found := false
	tr := tar.NewReader(rdr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			} else {
				return err
			}
		}
		if hdr.Name == source {
			file, err := os.Create(target)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := io.Copy(file, tr); err != nil {
				return err
			}
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("Not found file %s in targz", source)
	}

	return nil
}
