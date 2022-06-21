package opts

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

type OptKeyer interface {
	Match(*OptsExtractor) (string, bool)
}

type OptKey string

func (o OptKey) Match(opts *OptsExtractor) (string, bool) {
	if _, ok := opts.opts[string(o)]; ok {
		return string(o), true
	}
	return "", false
}

type OptKeyFunc func(key string) (bool, error)

func (o OptKeyFunc) Match(opts *OptsExtractor) (string, bool) {
	for k := range opts.opts {
		ok, err := o(k)
		if err != nil {
			opts.SetError(err)
		}
		if ok {
			return k, true
		}
	}
	return "", false
}

// Utility tool for extracting options from a map
//
// The Extract methods accept a keyer interface and a location to extract to,
// returning the exact key chosen, the parsed value, and an ok boolean.
//
// Errors detected during parsing are stored and deferred until the caller
// explicitly does so - this simplifies error checking by allowing it to all be
// performed at once.
type OptsExtractor struct {
	err  error
	opts map[string][]byte
}

func NewExtractor(opts map[string]string) *OptsExtractor {
	optsb := make(map[string][]byte)
	for k, v := range opts {
		optsb[k] = []byte(v)
	}
	return &OptsExtractor{
		opts: optsb,
	}
}

func NewExtractorBytes(opts map[string][]byte) *OptsExtractor {
	return &OptsExtractor{
		opts: opts,
	}
}

func (opts *OptsExtractor) ExtractString(key OptKeyer, result *string) (string, string, bool) {
	k, ok := key.Match(opts)
	if !ok {
		return "", "", false
	}
	r := opts.opts[k]
	if result != nil {
		*result = string(r)
	}
	delete(opts.opts, k)
	return k, string(r), true
}

func (opts *OptsExtractor) ExtractInt(key OptKeyer, result *int) (string, int, bool) {
	k, ok := key.Match(opts)
	if !ok {
		return "", 0, false
	}
	raw := opts.opts[k]
	delete(opts.opts, k)

	ii, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		opts.SetError(errors.Wrapf(err, "non-int value %s specified for %s", raw, k))
		return "", 0, false
	}
	if result != nil {
		*result = int(ii)
	}
	return k, int(ii), true
}

func (opts *OptsExtractor) ExtractBool(key OptKeyer, result *bool) (string, bool, bool) {
	k, ok := key.Match(opts)
	if !ok {
		return "", false, false
	}
	raw := opts.opts[k]
	delete(opts.opts, k)

	res, err := strconv.ParseBool(string(raw))
	if err != nil {
		opts.SetError(errors.Wrapf(err, "non-bool value specified for %s", k))
		return "", false, false
	}
	if result != nil {
		*result = res
	}
	return k, res, true
}

func (opts *OptsExtractor) ExtractBoolDefault(key OptKeyer, result *bool, def bool) (string, bool, bool) {
	k, ok := key.Match(opts)
	if !ok {
		return "", false, false
	}
	raw := opts.opts[k]
	delete(opts.opts, k)

	if len(raw) == 0 {
		*result = def
		return "", def, true
	}

	res, err := strconv.ParseBool(string(raw))
	if err != nil {
		opts.SetError(errors.Wrapf(err, "non-bool value specified for %s", k))
		return "", false, false
	}
	if result != nil {
		*result = res
	}
	return k, res, true
}

func (opts *OptsExtractor) Rest() map[string]string {
	result := make(map[string]string)
	for k, v := range opts.opts {
		result[k] = string(v)
	}
	return result
}

func (opts *OptsExtractor) RestBytes() map[string][]byte {
	return opts.opts
}

func (opts *OptsExtractor) Error() error {
	return opts.err
}

func (opts *OptsExtractor) SetError(err error) {
	if opts.err == nil {
		opts.err = err
	}
}

type ImageCommitOpts struct {
	ImageName      string
	RefCfg         cacheconfig.RefConfig
	OCITypes       bool
	BuildInfo      bool
	BuildInfoAttrs bool
	Annotations    AnnotationsGroup
}

func (c *ImageCommitOpts) Load(opts *OptsExtractor) {
	esgz := false

	opts.ExtractString(OptKey(keyImageName), &c.ImageName)

	if _, compType, ok := opts.ExtractString(OptKey(keyLayerCompression), nil); ok {
		switch compType {
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
			opts.SetError(errors.Errorf("unsupported layer compression type: %v", compType))
		}
	}
	opts.ExtractInt(OptKey(keyCompressionLevel), c.RefCfg.Compression.Level)
	opts.ExtractBoolDefault(OptKey(keyForceCompression), &c.RefCfg.Compression.Force, true)

	opts.ExtractBoolDefault(OptKey(keyOCITypes), &c.OCITypes, true)
	opts.ExtractBoolDefault(OptKey(keyBuildInfo), &c.BuildInfo, true)
	opts.ExtractBoolDefault(OptKey(keyBuildInfoAttrs), &c.BuildInfoAttrs, true)
	opts.ExtractBool(OptKey(keyPreferNondistLayers), &c.RefCfg.PreferNonDistributable)

	c.Annotations = ParseAnnotations(opts)

	if esgz && !c.OCITypes {
		logrus.Warn("forcibly turning on oci-mediatype mode for estargz")
		c.OCITypes = true
	}
}
