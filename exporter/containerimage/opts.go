package containerimage

import (
	"strconv"

	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/util/compression"
	"github.com/pkg/errors"
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
	ImageName      string
	RefCfg         cacheconfig.RefConfig
	OCITypes       bool
	BuildInfo      bool
	BuildInfoAttrs bool
	Annotations    AnnotationsGroup
}

func (c *ImageCommitOpts) Load(opt map[string]string) (map[string]string, error) {
	rest := make(map[string]string)

	esgz := false

	as, optb, err := ParseAnnotations(toBytesMap(opt))
	if err != nil {
		return nil, err
	}
	opt = toStringMap(optb)

	for k, v := range opt {
		var err error
		switch k {
		case keyImageName:
			c.ImageName = v
		case keyLayerCompression:
			switch v {
			case "gzip":
				c.RefCfg.Compression.Type = compression.Gzip
			case "estargz":
				c.RefCfg.Compression.Type = compression.EStargz
				esgz = true
			case "zstd":
				c.RefCfg.Compression.Type = compression.Zstd
			case "uncompressed":
				c.RefCfg.Compression.Type = compression.Uncompressed
			default:
				err = errors.Errorf("unsupported layer compression type: %v", v)
			}
		case keyCompressionLevel:
			ii, err2 := strconv.ParseInt(v, 10, 64)
			if err != nil {
				err = errors.Wrapf(err2, "non-int value %s specified for %s", v, k)
				break
			}
			v := int(ii)
			c.RefCfg.Compression.Level = &v
		case keyForceCompression:
			err = parseBoolWithDefault(&c.RefCfg.Compression.Force, k, v, true)
		case keyOCITypes:
			err = parseBoolWithDefault(&c.OCITypes, k, v, true)
		case keyBuildInfo:
			err = parseBoolWithDefault(&c.BuildInfo, k, v, true)
		case keyBuildInfoAttrs:
			err = parseBoolWithDefault(&c.BuildInfoAttrs, k, v, false)
		case keyPreferNondistLayers:
			err = parseBool(&c.RefCfg.PreferNonDistributable, k, v)
		default:
			rest[k] = v
		}

		if err != nil {
			return nil, err
		}
	}

	if esgz {
		c.EnableOCITypes("estargz")
	}

	c.AddAnnotations(as)

	return rest, nil
}

func (c *ImageCommitOpts) AddAnnotations(annotations AnnotationsGroup) {
	if annotations == nil {
		return
	}
	if c.Annotations == nil {
		c.Annotations = AnnotationsGroup{}
	}
	c.Annotations = c.Annotations.Merge(annotations)
	for _, a := range annotations {
		if len(a.Index)+len(a.IndexDescriptor)+len(a.ManifestDescriptor) > 0 {
			c.EnableOCITypes("annotations")
		}
	}
}

func (c *ImageCommitOpts) EnableOCITypes(reason string) {
	if !c.OCITypes {
		message := "forcibly turning on oci-mediatype mode"
		if reason != "" {
			message += " for " + reason
		}
		logrus.Warn(message)

		c.OCITypes = true
	}
}

func parseBool(dest *bool, key string, value string) error {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return errors.Wrapf(err, "non-bool value specified for %s", key)
	}
	*dest = b
	return nil
}

func parseBoolWithDefault(dest *bool, key string, value string, defaultValue bool) error {
	if value == "" {
		*dest = defaultValue
		return nil
	}
	return parseBool(dest, key, value)
}

func toBytesMap(m map[string]string) map[string][]byte {
	result := make(map[string][]byte)
	for k, v := range m {
		result[k] = []byte(v)
	}
	return result
}

func toStringMap(m map[string][]byte) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		result[k] = string(v)
	}
	return result
}
