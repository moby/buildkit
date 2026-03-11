package proc

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/moby/buildkit/executor/resources"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	artifactfrontend "github.com/moby/buildkit/frontend/artifact"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver"
	solverpb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/tracing"
	"github.com/pkg/errors"
)

func ArtifactProcessor() llbsolver.Processor {
	return func(ctx context.Context, res *llbsolver.Result, s *llbsolver.Solver, j *solver.Job, usage *resources.SysSampler) (_ *llbsolver.Result, err error) {
		if _, ok := res.Metadata[exptypes.ExporterArtifactKey]; !ok {
			return res, nil
		}

		span, ctx := tracing.StartSpan(ctx, "create artifact OCI layout")
		defer span.End()

		if len(res.Refs) > 1 {
			return nil, errors.New("artifact processor requires exactly one ref")
		}

		ref, ok := res.FindRef("")
		if !ok || ref == nil {
			return nil, errors.New("artifact processor requires exactly one ref")
		}

		postRes, err := s.SolvePostprocessor(ctx, j, frontend.SolveRequest{
			Frontend: artifactfrontend.Name,
			FrontendOpt: map[string]string{
				exptypes.ExporterArtifactTypeKey:       string(res.Metadata[exptypes.ExporterArtifactTypeKey]),
				exptypes.ExporterArtifactConfigTypeKey: string(res.Metadata[exptypes.ExporterArtifactConfigTypeKey]),
				exptypes.ExporterArtifactLayersKey:     string(res.Metadata[exptypes.ExporterArtifactLayersKey]),
			},
			FrontendInputs: map[string]*solverpb.Definition{
				artifactfrontend.InputKey: ref.Definition(),
			},
		})
		if err != nil {
			return nil, errors.Wrap(err, "artifact processor: failed to run artifact frontend")
		}

		if err := updateArtifactProvenance(res, postRes); err != nil {
			return nil, err
		}
		postMeta := maps.Clone(res.Metadata)
		if err := normalizeArtifactResultMetadata(postMeta); err != nil {
			return nil, err
		}
		postMeta[exptypes.ExporterOCILayoutKey] = []byte("true")
		delete(postMeta, exptypes.ExporterArtifactKey)
		delete(postMeta, exptypes.ExporterArtifactTypeKey)
		delete(postMeta, exptypes.ExporterArtifactConfigTypeKey)
		delete(postMeta, exptypes.ExporterArtifactLayersKey)
		if postRes.Result.Metadata == nil {
			postRes.Result.Metadata = map[string][]byte{}
		}
		maps.Copy(postRes.Result.Metadata, postMeta)
		if len(res.Attestations) > 0 {
			if postRes.Result.Attestations == nil {
				postRes.Result.Attestations = map[string][]frontend.Attestation{}
			}
			maps.Copy(postRes.Result.Attestations, res.Attestations)
		}

		res.Result = postRes.Result

		if err := ref.Release(context.WithoutCancel(ctx)); err != nil {
			return nil, errors.Wrap(err, "artifact processor: failed to release original result ref")
		}

		return res, nil
	}
}

func updateArtifactProvenance(res *llbsolver.Result, postRes *llbsolver.Result) error {
	if postRes == nil || postRes.Provenance == nil {
		if res.Provenance != nil {
			res.Provenance.Refs = nil
		}
		return nil
	}
	if res.Provenance == nil {
		res.Provenance = postRes.Provenance
		res.Provenance.Refs = nil
		return nil
	}

	oldProv := res.Provenance.Ref
	newProv := postRes.Provenance.Ref
	if newProv == nil {
		res.Provenance.Ref = oldProv
		res.Provenance.Refs = nil
		return nil
	}
	if oldProv != nil {
		postprocess := newProv.Parameters()
		if err := newProv.Merge(oldProv); err != nil {
			return errors.Wrap(err, "artifact processor: failed to merge provenance")
		}
		newProv.AddPostprocess(postprocess)
		newProv.Frontend = oldProv.Frontend
		newProv.Args = maps.Clone(oldProv.Args)
	}

	res.Provenance = postRes.Provenance
	res.Provenance.Ref = newProv
	res.Provenance.Refs = nil
	return nil
}

func normalizeArtifactResultMetadata(meta map[string][]byte) error {
	if meta == nil {
		return nil
	}

	platformsBytes, ok := meta[exptypes.ExporterPlatformsKey]
	if !ok {
		return nil
	}

	var platforms exptypes.Platforms
	if err := json.Unmarshal(platformsBytes, &platforms); err != nil {
		return errors.Wrap(err, "artifact processor: failed to parse platforms metadata")
	}
	if len(platforms.Platforms) != 1 {
		return errors.Errorf("artifact processor: expected exactly one platform mapping, got %d", len(platforms.Platforms))
	}

	platformID := platforms.Platforms[0].ID
	for _, key := range exptypes.KnownRefMetadataKeys {
		if _, ok := meta[key]; ok {
			continue
		}
		if v, ok := meta[key+"/"+platformID]; ok {
			meta[key] = v
		}
	}
	return nil
}
