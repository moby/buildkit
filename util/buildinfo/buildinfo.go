package buildinfo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/source"
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	"github.com/moby/buildkit/util/urlutil"
	"github.com/pkg/errors"
)

// Decode decodes a base64 encoded build info.
func Decode(enc string) (bi binfotypes.BuildInfo, _ error) {
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return bi, err
	}
	err = json.Unmarshal(dec, &bi)
	return bi, err
}

// Encode encodes build info.
func Encode(ctx context.Context, req frontend.SolveRequest, buildSources map[string]string, imageConfig []byte) ([]byte, error) {
	icbi, err := FromImageConfig(imageConfig)
	if err != nil {
		return nil, err
	}
	srcs, err := mergeSources(ctx, buildSources, icbi)
	if err != nil {
		return nil, err
	}
	return json.Marshal(binfotypes.BuildInfo{
		Frontend: req.Frontend,
		Attrs:    filterAttrs(req.FrontendOpt),
		Sources:  srcs,
	})
}

// mergeSources combines and fixes build sources from image config
// key binfotypes.ImageConfigField.
func mergeSources(ctx context.Context, buildSources map[string]string, imageConfigBuildInfo binfotypes.BuildInfo) ([]binfotypes.Source, error) {
	// Iterate and combine build sources
	mbs := map[string]binfotypes.Source{}
	for buildSource, pin := range buildSources {
		src, err := source.FromString(buildSource)
		if err != nil {
			return nil, err
		}
		switch sourceID := src.(type) {
		case *source.ImageIdentifier:
			for i, ics := range imageConfigBuildInfo.Sources {
				// Use original user input from image config
				if ics.Type == binfotypes.SourceTypeDockerImage && ics.Alias == sourceID.Reference.String() {
					if _, ok := mbs[ics.Alias]; !ok {
						parsed, err := reference.ParseNormalizedNamed(ics.Ref)
						if err != nil {
							return nil, errors.Wrapf(err, "failed to parse %s", ics.Ref)
						}
						mbs[ics.Alias] = binfotypes.Source{
							Type: binfotypes.SourceTypeDockerImage,
							Ref:  reference.TagNameOnly(parsed).String(),
							Pin:  pin,
						}
						imageConfigBuildInfo.Sources = append(imageConfigBuildInfo.Sources[:i], imageConfigBuildInfo.Sources[i+1:]...)
					}
					break
				}
			}
			if _, ok := mbs[sourceID.Reference.String()]; !ok {
				mbs[sourceID.Reference.String()] = binfotypes.Source{
					Type: binfotypes.SourceTypeDockerImage,
					Ref:  sourceID.Reference.String(),
					Pin:  pin,
				}
			}
		case *source.GitIdentifier:
			sref := sourceID.Remote
			if len(sourceID.Ref) > 0 {
				sref += "#" + sourceID.Ref
			}
			if len(sourceID.Subdir) > 0 {
				sref += ":" + sourceID.Subdir
			}
			if _, ok := mbs[sref]; !ok {
				mbs[sref] = binfotypes.Source{
					Type: binfotypes.SourceTypeGit,
					Ref:  urlutil.RedactCredentials(sref),
					Pin:  pin,
				}
			}
		case *source.HTTPIdentifier:
			if _, ok := mbs[sourceID.URL]; !ok {
				mbs[sourceID.URL] = binfotypes.Source{
					Type: binfotypes.SourceTypeHTTP,
					Ref:  urlutil.RedactCredentials(sourceID.URL),
					Pin:  pin,
				}
			}
		}
	}

	// Leftovers build deps in image config. Mostly duplicated ones we
	// don't need but there is an edge case if no instruction except sources
	// one is defined (e.g. FROM ...) that can be valid so take it into account.
	for _, ics := range imageConfigBuildInfo.Sources {
		if ics.Type != binfotypes.SourceTypeDockerImage {
			continue
		}
		if _, ok := mbs[ics.Alias]; !ok {
			parsed, err := reference.ParseNormalizedNamed(ics.Ref)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse %s", ics.Ref)
			}
			mbs[ics.Alias] = binfotypes.Source{
				Type: binfotypes.SourceTypeDockerImage,
				Ref:  reference.TagNameOnly(parsed).String(),
				Pin:  ics.Pin,
			}
		}
	}

	srcs := make([]binfotypes.Source, 0, len(mbs))
	for _, bs := range mbs {
		srcs = append(srcs, bs)
	}
	sort.Slice(srcs, func(i, j int) bool {
		return srcs[i].Ref < srcs[j].Ref
	})

	return srcs, nil
}

// FromImageConfig returns build dependencies from image config.
func FromImageConfig(imageConfig []byte) (bi binfotypes.BuildInfo, _ error) {
	if len(imageConfig) == 0 {
		return bi, nil
	}
	var config binfotypes.ImageConfig
	if err := json.Unmarshal(imageConfig, &config); err != nil {
		return bi, errors.Wrap(err, "failed to unmarshal buildinfo from image config")
	}
	if len(config.BuildInfo) == 0 {
		return bi, nil
	}
	if err := json.Unmarshal(config.BuildInfo, &bi); err != nil {
		return bi, errors.Wrap(err, "failed to unmarshal buildinfo")
	}
	return bi, nil
}

// FormatOpts holds build info format options.
type FormatOpts struct {
	RemoveAttrs bool
}

// Format formats build info.
func Format(dt []byte, format FormatOpts) (_ []byte, err error) {
	if len(dt) == 0 {
		return dt, nil
	}
	var bi binfotypes.BuildInfo
	if err := json.Unmarshal(dt, &bi); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal buildinfo for formatting")
	}
	if format.RemoveAttrs {
		bi.Attrs = nil
	}
	if dt, err = json.Marshal(bi); err != nil {
		return nil, err
	}
	return dt, nil
}

var knownAttrs = []string{
	"cmdline",
	"context",
	"filename",
	"source",

	//"add-hosts",
	//"cgroup-parent",
	//"force-network-mode",
	//"hostname",
	//"image-resolve-mode",
	"platform",
	"shm-size",
	"target",
	"ulimit",
}

// filterAttrs filters frontent opt by picking only those that
// could effectively change the build result.
func filterAttrs(attrs map[string]string) map[string]string {
	filtered := make(map[string]string)
	for k, v := range attrs {
		// Control args are filtered out
		if isControlArg(k) {
			continue
		}
		// Always include args and labels
		if strings.HasPrefix(k, "build-arg:") || strings.HasPrefix(k, "label:") {
			filtered[k] = v
			continue
		}
		// Filter only for known attributes
		for _, knownAttr := range knownAttrs {
			if knownAttr == k {
				filtered[k] = v
				break
			}
		}
	}
	return filtered
}

var knownControlArgs = []string{
	"BUILDKIT_CACHE_MOUNT_NS",
	"BUILDKIT_CONTEXT_KEEP_GIT_DIR",
	"BUILDKIT_INLINE_BUILDINFO_ATTRS",
	"BUILDKIT_INLINE_CACHE",
	"BUILDKIT_MULTI_PLATFORM",
	"BUILDKIT_SANDBOX_HOSTNAME",
	"BUILDKIT_SYNTAX",
}

// isControlArg checks if a build attributes is a control arg
func isControlArg(attrKey string) bool {
	for _, k := range knownControlArgs {
		if strings.HasPrefix(attrKey, "build-arg:"+k) {
			return true
		}
	}
	return false
}
