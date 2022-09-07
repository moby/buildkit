package proc

import (
	"context"
	"encoding/json"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/attest"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver"
	"github.com/pkg/errors"
)

func SBOMProcessor(scannerRef string) llbsolver.Processor {
	return func(ctx context.Context, res *llbsolver.Result, s *llbsolver.Solver, j *solver.Job) (*llbsolver.Result, error) {
		// skip sbom generation if we already have an sbom
		if attest.HasSBOM(res.Result) {
			return res, nil
		}

		platformsBytes, ok := res.Metadata[exptypes.ExporterPlatformsKey]
		if !ok {
			return nil, errors.Errorf("unable to collect multiple refs, missing platforms mapping")
		}

		var ps exptypes.Platforms
		if len(platformsBytes) > 0 {
			if err := json.Unmarshal(platformsBytes, &ps); err != nil {
				return nil, errors.Wrapf(err, "failed to parse platforms passed to sbom processor")
			}
		}

		scanner, err := attest.CreateSBOMScanner(ctx, s.Bridge(j), scannerRef)
		if err != nil {
			return nil, err
		}
		if scanner == nil {
			return res, nil
		}

		for _, p := range ps.Platforms {
			ref, ok := res.Refs[p.ID]
			if !ok {
				return nil, errors.Errorf("could not find ref %s", p.ID)
			}
			defop, err := llb.NewDefinitionOp(ref.Definition())
			if err != nil {
				return nil, err
			}
			st := llb.NewState(defop)

			att, st, err := scanner(ctx, p.ID, st, nil)
			if err != nil {
				return nil, err
			}

			def, err := st.Marshal(ctx)
			if err != nil {
				return nil, err
			}

			r, err := s.Bridge(j).Solve(ctx, frontend.SolveRequest{ // TODO: buildinfo
				Definition: def.ToPB(),
			}, j.SessionID)
			if err != nil {
				return nil, err
			}
			res.AddAttestation(p.ID, att, r.Ref)
		}
		return res, nil
	}
}
