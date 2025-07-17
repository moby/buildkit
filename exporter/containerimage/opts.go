package containerimage

import (
	"context"
	"strconv"
	"time"

	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/exporter/util/epoch"
	"github.com/moby/buildkit/util/compression"
	"github.com/pkg/errors"
)

type ImageCommitOpts struct {
	ImageName   string
	RefCfg      cacheconfig.RefConfig
	OCITypes    bool
	OCIArtifact bool
	Annotations AnnotationsGroup
	Epoch       *time.Time

	ForceInlineAttestations bool // force inline attestations to be attached
	RewriteTimestamp        bool // rewrite timestamps in layers to match the epoch
}

func (c *ImageCommitOpts) Load(ctx context.Context, opt map[string]string) (map[string]string, error) {
	rest := make(map[string]string)

	as, optb, err := ParseAnnotations(toBytesMap(opt))
	if err != nil {
		return nil, err
	}
	opt = toStringMap(optb)

	c.Epoch, opt, err = epoch.ParseExporterAttrs(opt)
	if err != nil {
		return nil, err
	}

	if c.RefCfg.Compression, err = compression.ParseAttributes(opt); err != nil {
		return nil, err
	}

	for k, v := range opt {
		var err error
		switch exptypes.ImageExporterOptKey(k) {
		case exptypes.OptKeyName:
			c.ImageName = v
		case exptypes.OptKeyOCITypes:
			err = parseBool(&c.OCITypes, k, v)
		case exptypes.OptKeyOCIArtifact:
			err = parseBool(&c.OCIArtifact, k, v)
		case exptypes.OptKeyForceInlineAttestations:
			err = parseBool(&c.ForceInlineAttestations, k, v)
		case exptypes.OptKeyPreferNondistLayers:
			err = parseBool(&c.RefCfg.PreferNonDistributable, k, v)
		case exptypes.OptKeyRewriteTimestamp:
			err = parseBool(&c.RewriteTimestamp, k, v)
		default:
			rest[k] = v
		}

		if err != nil {
			return nil, err
		}
	}

	if c.RefCfg.Compression.Type.OnlySupportOCITypes() && !c.OCITypes {
		return nil, errors.Errorf("exporter option \"compression=%s\" conflicts with \"oci-mediatypes=false\"", c.RefCfg.Compression.Type)
	}
	if c.OCIArtifact && !c.OCITypes {
		return nil, errors.New("exporter option \"oci-artifact=true\" conflicts with \"oci-mediatypes=false\"")
	}

	c.Annotations = c.Annotations.Merge(as)

	return rest, nil
}

func parseBool(dest *bool, key string, value string) error {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return errors.Wrapf(err, "non-bool value specified for %s", key)
	}
	*dest = b
	return nil
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
