package proc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// ForceRefsProcessor is a processor that forces a result made of a single Ref
// into the Refs map, correctly creating and converting relevant metadata.
//
// This is useful for cases where a frontend produces a single-platform image,
// but we need to add additional Refs to it (e.g. attestations).
func ForceRefsProcessor(ctx context.Context, result *frontend.Result, s *llbsolver.Solver, j *solver.Job) (*frontend.Result, error) {
	if len(result.Refs) > 0 {
		return result, nil
	}
	if result.Ref == nil {
		return nil, errors.New("no refs to operate on")
	}

	// try to determine platform from image config, fallback to current platform
	p := platforms.DefaultSpec()
	if imgConfig, ok := result.Metadata[exptypes.ExporterImageConfigKey]; ok {
		var img ocispecs.Image
		err := json.Unmarshal(imgConfig, &img)
		if err != nil {
			return nil, err
		}

		if img.OS != "" && img.Architecture != "" {
			p = ocispecs.Platform{
				Architecture: img.Architecture,
				OS:           img.OS,
				OSVersion:    img.OSVersion,
				OSFeatures:   img.OSFeatures,
				Variant:      img.Variant,
			}
		}
	}
	p = platforms.Normalize(p)
	pk := platforms.Format(p)

	result.Refs = map[string]solver.ResultProxy{
		pk: result.Ref,
	}
	result.Ref = nil

	if result.Metadata != nil {
		for _, key := range exptypes.KnownRefMetadataKeys {
			if value, ok := result.Metadata[key]; ok {
				result.Metadata[fmt.Sprintf("%s/%s", key, pk)] = value
				delete(result.Metadata, key)
			}
		}
	}

	expPlatforms := exptypes.Platforms{
		Platforms: []exptypes.Platform{{ID: pk, Platform: p}},
	}
	dt, err := json.Marshal(expPlatforms)
	if err != nil {
		return nil, err
	}
	result.AddMeta(exptypes.ExporterPlatformsKey, dt)

	return result, nil
}
