package nydus

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"

	"github.com/dragonflyoss/image-service/contrib/nydusify/pkg/converter"
	"github.com/dragonflyoss/image-service/contrib/nydusify/pkg/converter/provider"
)

const builderName = "nydus-image"

const (
	// Specify Nydus image reference.
	keyTargetRef = "name"
	// Merge Nydus image manifest with the OCI image manifest
	// exists in remote registry into an manifest index.
	keyMergeManifest = "merge-manifest"
	// Set target manifest media types following OCI spec.
	keyOCIMediaTypes = "oci-mediatypes"
	// Push to insecure HTTP registry.
	keyInsecure = "registry.insecure"
)

type Opt struct {
	ImageOpt     containerimage.Opt
	CacheManager cache.Manager
}

type nydusExporter struct {
	opt Opt
}

type nydusExporterInstance struct {
	*nydusExporter

	nydusBuilder  string
	targetRef     string
	insecure      bool
	mergeManifest bool
	ociMediaTypes bool
}

func New(opt Opt) (exporter.Exporter, error) {
	return &nydusExporter{opt: opt}, nil
}

func (exporter *nydusExporter) Resolve(ctx context.Context, opt map[string]string) (exporter.ExporterInstance, error) {
	// Nydus exporter relies on the Nydus builder to build the
	// image layer to a Nydus image layer.
	if _, err := exec.LookPath(builderName); err != nil {
		return nil, errors.Wrap(err, "not found nydus builder in PATH environment variable")
	}

	instance := &nydusExporterInstance{
		nydusExporter: exporter,
		nydusBuilder:  builderName,
	}

	for k, v := range opt {
		switch k {
		case keyTargetRef:
			instance.targetRef = v
		case keyInsecure:
			if v == "" {
				instance.insecure = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value specified for %s", k)
			}
			instance.insecure = b
		case keyMergeManifest:
			if v == "" || v == "true" {
				instance.mergeManifest = true
			}
		case keyOCIMediaTypes:
			if v == "" || v == "true" {
				instance.ociMediaTypes = true
			}
		}
	}

	return instance, nil
}

// Mount a temp directory to save intermediate of Nydus image during exporting
func (exporter *nydusExporterInstance) createTempDir(ctx context.Context, sessionGroup session.Group) (string, func() error, error) {
	cacheManager := exporter.opt.CacheManager

	mountableRef, err := cacheManager.New(
		ctx, nil, sessionGroup, cache.WithDescription("nydus exporter intermediate"),
	)
	if err != nil {
		return "", nil, errors.Wrap(err, "create mountable ref")
	}
	mountable, err := mountableRef.Mount(ctx, false, sessionGroup)
	if err != nil {
		return "", nil, errors.Wrap(err, "get mountable from mountable ref")
	}

	mounter := snapshot.LocalMounter(mountable)
	dir, err := mounter.Mount()
	if err != nil {
		return "", nil, errors.Wrap(err, "mount from mountable ref")
	}

	return dir, mounter.Unmount, nil
}

// Source prepares image config and layers for Nydusify building
func (exporter *nydusExporterInstance) getSources(inp exporter.Source, sessionID string) ([]provider.SourceProvider, error) {
	sources := []provider.SourceProvider{}

	configBytesAry := [][]byte{}
	refs := []cache.ImmutableRef{}
	configBytes := inp.Metadata[exptypes.ExporterImageConfigKey]
	if configBytes != nil && inp.Ref != nil {
		configBytesAry = append(configBytesAry, configBytes)
		refs = append(refs, inp.Ref)
	}

	for key, data := range inp.Metadata {
		keyAry := strings.Split(key, "/")
		if len(keyAry) != 2 || keyAry[0] != exptypes.ExporterImageConfigKey {
			continue
		}
		platform := keyAry[1]
		if inp.Refs[platform] == nil {
			continue
		}
		configBytesAry = append(configBytesAry, data)
		refs = append(refs, inp.Refs[platform])
	}

	for idx, configBytes := range configBytesAry {
		var config ocispec.Image
		if err := json.Unmarshal(configBytes, &config); err != nil {
			return nil, errors.New("unmarshal source image config")
		}
		source := &sourceProvider{
			ref:       refs[idx],
			sessionID: sessionID,
			config:    config,
		}
		sources = append(sources, source)
	}

	return sources, nil
}

func (exporter *nydusExporterInstance) Name() string {
	return fmt.Sprintf("Exporting Nydus image to %s", exporter.targetRef)
}

func (exporter *nydusExporterInstance) Export(
	ctx context.Context, inp exporter.Source, sessionID string,
) (map[string]string, error) {
	sources, err := exporter.getSources(inp, sessionID)
	if err != nil {
		return nil, errors.Wrap(err, "get image sources")
	}

	// The remote instance communicates with registry
	targetRemote, err := NewRemote(
		exporter.opt.ImageOpt.SessionManager, sessionID, exporter.opt.ImageOpt.RegistryHosts, exporter.targetRef, exporter.insecure,
	)
	if err != nil {
		return nil, errors.Wrap(err, "create target remote")
	}

	// Prepare temp directory for Nydus builder, the builder will output
	// blob/bootstrap intermediate of Nydus image to this directory
	workDir, umount, err := exporter.createTempDir(ctx, session.NewGroup(sessionID))
	if err != nil {
		return nil, errors.Wrap(err, "create work directory")
	}
	defer umount()

	cvt, err := converter.New(converter.Opt{
		Logger:          &progressLogger{},
		SourceProviders: sources,
		TargetRemote:    targetRemote,
		WorkDir:         workDir,
		NydusImagePath:  exporter.nydusBuilder,
		MultiPlatform:   exporter.mergeManifest,
		DockerV2Format:  !exporter.ociMediaTypes,
	})
	if err != nil {
		return nil, err
	}

	if err := cvt.Convert(ctx); err != nil {
		return nil, err
	}

	return nil, nil
}
