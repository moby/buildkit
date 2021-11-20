package buildinfo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"

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

// Merge combines and fixes build sources from image config
// key binfotypes.ImageConfigField.
func Merge(ctx context.Context, buildSources map[string]string, imageConfig []byte) ([]byte, error) {
	icbi, err := FromImageConfig(imageConfig)
	if err != nil {
		return nil, err
	}

	// Iterate and combine build sources
	mbs := map[string]binfotypes.Source{}
	for buildSource, pin := range buildSources {
		src, err := source.FromString(buildSource)
		if err != nil {
			return nil, err
		}
		switch sourceID := src.(type) {
		case *source.ImageIdentifier:
			for i, ics := range icbi.Sources {
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
						icbi.Sources = append(icbi.Sources[:i], icbi.Sources[i+1:]...)
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
	for _, ics := range icbi.Sources {
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

	return json.Marshal(binfotypes.BuildInfo{
		Sources: srcs,
	})
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
