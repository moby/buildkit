package containerimage

import (
	"sync"

	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/util/compression"
	"github.com/sirupsen/logrus"
)

const (
	keyImageName        = "name"
	keyLayerCompression = "compression"
	keyCompressionLevel = "compression-level"
	keyForceCompression = "force-compression"
	keyOCITypes         = "oci-mediatypes"
	keyBuildInfo        = "buildinfo"
	keyBuildInfoAttrs   = "buildinfo-attrs"

	// preferNondistLayersKey is an exporter option which can be used to mark a layer as non-distributable if the layer reference was
	// already found to use a non-distributable media type.
	// When this option is not set, the exporter will change the media type of the layer to a distributable one.
	keyPreferNondistLayers = "prefer-nondist-layers"
)

type ImageCommitOpts struct {
	ImageName string `opt:"name" name:"Image name" help:"Location reference for the image result to be pushed to."`

	Compression struct {
		Type  string `opt:"compression,gzip/estargz/zstd/uncompressed" name:"Compression type"`
		Level *int   `opt:"compression-level" name:"Compression level"`
		Force bool   `opt:"compression-force" name:"Force compression"`
	} `opt:",squash" name:"Compression"`

	OCITypes bool `opt:"oci-mediatypes" name:"OCI Media Types" help:"Use OCI media types for image output"`

	BuildInfo struct {
		Enable bool `opt:"buildinfo" name:"Build Info" help:"Attach inline buildinfo"`
		Attrs  bool `opt:"buildinfo-attrs" name:"Build Info Attributes" help:"Attach inline buildinfo attributes"`
	} `opt:",squash" name:"Build Info"`

	PreferNonDistributable bool `opt:"prefer-nondist-layers"`

	Annotations AnnotationsGroup `opt:"annotation*" name:"Annotations"`

	Meta map[string]string `opt:"-,remain"`

	logOnce sync.Once
}

func (opts *ImageCommitOpts) UnmarshalAnnotations(key string, value string) error {
	if opts.Annotations == nil {
		opts.Annotations = AnnotationsGroup{}
	}
	return opts.Annotations.Load(key, value)
}

func (c *ImageCommitOpts) OCI() bool {
	if c.OCITypes {
		return true
	}
	if c.Compression.Type == "estargz" {
		c.logOnce.Do(func() {
			logrus.Warn("forcibly turning on oci-mediatype mode for estargz")
		})
		return true
	}
	for _, a := range c.Annotations {
		c.logOnce.Do(func() {
			logrus.Warn("forcibly turning on oci-mediatype mode for annotations")
		})
		if len(a.Index)+len(a.IndexDescriptor)+len(a.ManifestDescriptor) > 0 {
			return true
		}
	}
	return false
}

func (c *ImageCommitOpts) RefCfg() cacheconfig.RefConfig {
	return cacheconfig.RefConfig{
		Compression: compression.Config{
			Type:  compression.Parse(c.Compression.Type),
			Level: c.Compression.Level,
			Force: c.Compression.Force,
		},
		PreferNonDistributable: c.PreferNonDistributable,
	}
}
