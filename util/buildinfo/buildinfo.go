package buildinfo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"github.com/docker/distribution/reference"
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
func Encode(ctx context.Context, buildInfo []byte, buildSources map[string]string) ([]byte, error) {
	var bi binfotypes.BuildInfo
	if buildInfo != nil {
		if err := json.Unmarshal(buildInfo, &bi); err != nil {
			return nil, err
		}
	}
	msources, err := mergeSources(ctx, buildSources, bi.Sources)
	if err != nil {
		return nil, err
	}
	return json.Marshal(binfotypes.BuildInfo{
		Frontend: bi.Frontend,
		Attrs:    filterAttrs(bi.Attrs),
		Sources:  msources,
	})
}

// mergeSources combines and fixes build sources from frontend sources.
func mergeSources(ctx context.Context, buildSources map[string]string, frontendSources []binfotypes.Source) ([]binfotypes.Source, error) {
	// Iterate and combine build sources
	mbs := map[string]binfotypes.Source{}
	for buildSource, pin := range buildSources {
		src, err := source.FromString(buildSource)
		if err != nil {
			return nil, err
		}
		switch sourceID := src.(type) {
		case *source.ImageIdentifier:
			for i, fsrc := range frontendSources {
				// use original user input from frontend sources
				if fsrc.Type == binfotypes.SourceTypeDockerImage && fsrc.Alias == sourceID.Reference.String() {
					if _, ok := mbs[fsrc.Alias]; !ok {
						parsed, err := reference.ParseNormalizedNamed(fsrc.Ref)
						if err != nil {
							return nil, errors.Wrapf(err, "failed to parse %s", fsrc.Ref)
						}
						mbs[fsrc.Alias] = binfotypes.Source{
							Type: binfotypes.SourceTypeDockerImage,
							Ref:  reference.TagNameOnly(parsed).String(),
							Pin:  pin,
						}
						frontendSources = append(frontendSources[:i], frontendSources[i+1:]...)
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

	// leftover sources in frontend. Mostly duplicated ones we don't need but
	// there is an edge case if no instruction except sources one is defined
	// (e.g. FROM ...) that can be valid so take it into account.
	for _, fsrc := range frontendSources {
		if fsrc.Type != binfotypes.SourceTypeDockerImage {
			continue
		}
		if _, ok := mbs[fsrc.Alias]; !ok {
			parsed, err := reference.ParseNormalizedNamed(fsrc.Ref)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse %s", fsrc.Ref)
			}
			mbs[fsrc.Alias] = binfotypes.Source{
				Type: binfotypes.SourceTypeDockerImage,
				Ref:  reference.TagNameOnly(parsed).String(),
				Pin:  fsrc.Pin,
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
	//"cmdline",
	"context",
	"filename",
	"source",

	//"add-hosts",
	//"cgroup-parent",
	//"force-network-mode",
	//"hostname",
	//"image-resolve-mode",
	//"platform",
	"shm-size",
	"target",
	"ulimit",
}

// filterAttrs filters frontent opt by picking only those that
// could effectively change the build result.
func filterAttrs(attrs map[string]*string) map[string]*string {
	filtered := make(map[string]*string)
	for k, v := range attrs {
		if v == nil {
			continue
		}
		// Control args are filtered out
		if isControlArg(k) {
			continue
		}
		// Always include
		if strings.HasPrefix(k, "build-arg:") || strings.HasPrefix(k, "context:") || strings.HasPrefix(k, "label:") {
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

// GetMetadata returns buildinfo metadata for the specified key. If the key
// is already there, result will be merged.
func GetMetadata(metadata map[string][]byte, key string, reqFrontend string, reqAttrs map[string]string) ([]byte, error) {
	var (
		dtbi []byte
		err  error
	)
	if v, ok := metadata[key]; ok {
		var mbi binfotypes.BuildInfo
		if errm := json.Unmarshal(v, &mbi); errm != nil {
			return nil, errors.Wrapf(errm, "failed to unmarshal build info for %q", key)
		}
		if reqFrontend != "" {
			mbi.Frontend = reqFrontend
		}
		mbi.Attrs = filterAttrs(convertMap(reduceMap(reqAttrs, mbi.Attrs)))
		dtbi, err = json.Marshal(mbi)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal build info for %q", key)
		}
	} else {
		dtbi, err = json.Marshal(binfotypes.BuildInfo{
			Frontend: reqFrontend,
			Attrs:    filterAttrs(convertMap(reqAttrs)),
		})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal build info for %q", key)
		}
	}
	return dtbi, nil
}

func reduceMap(m1 map[string]string, m2 map[string]*string) map[string]string {
	if m1 == nil && m2 == nil {
		return nil
	}
	if m1 == nil {
		m1 = map[string]string{}
	}
	for k, v := range m2 {
		if v != nil {
			m1[k] = *v
		}
	}
	return m1
}

func convertMap(m map[string]string) map[string]*string {
	res := make(map[string]*string)
	for k, v := range m {
		value := v
		res[k] = &value
	}
	return res
}
