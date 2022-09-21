package proc

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	slsa "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver"
	"github.com/moby/buildkit/solver/result"
	binfotypes "github.com/moby/buildkit/util/buildinfo/types"
	provenance "github.com/moby/buildkit/util/provenance"
	"github.com/pkg/errors"
)

var BuildKitBuildType = "https://mobyproject.org/buildkit@v1"

func ProvenanceProcessor(attrs map[string]string) llbsolver.Processor {
	return func(ctx context.Context, res *frontend.Result, s *llbsolver.Solver, j *solver.Job) (*frontend.Result, error) {
		if len(res.Refs) == 0 {
			return nil, errors.New("provided result has no refs")
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

		buildID := identity.NewID()

		var reproducible bool
		if v, ok := attrs["reproducible"]; ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse reproducible flag %q", v)
			}
			reproducible = b
		}

		var mode string
		if v, ok := attrs["mode"]; ok {
			switch v {
			case "disabled", "none":
				return res, nil
			case "full":
				mode = "max"
			case "max", "min":
				mode = v
			default:
				return nil, errors.Errorf("invalid mode %q", v)
			}
		}

		for _, p := range ps.Platforms {
			dt, ok := res.Metadata[exptypes.ExporterBuildInfo+"/"+p.ID]
			if !ok {
				return nil, errors.New("no build info found for provenance")
			}

			var bi binfotypes.BuildInfo
			if err := json.Unmarshal(dt, &bi); err != nil {
				return nil, errors.Wrap(err, "failed to parse build info")
			}

			pr, err := provenance.FromBuildInfo(bi)
			if err != nil {
				return nil, err
			}

			st := j.StartedTime()

			pr.Metadata.BuildStartedOn = &st
			pr.Metadata.Reproducible = reproducible
			pr.Metadata.BuildInvocationID = buildID

			if mode != "max" {
				param := make(map[string]*string)
				for k, v := range pr.Invocation.Parameters.(map[string]*string) {
					if strings.HasPrefix(k, "build-arg:") || strings.HasPrefix(k, "label:") {
						pr.Metadata.Completeness.Parameters = false
						continue
					}
					param[k] = v
				}
				pr.Invocation.Parameters = param
			} else {
				if err := provenance.AddBuildConfig(ctx, pr, res.Refs[p.ID]); err != nil {
					return nil, err
				}
			}

			res.AddAttestation(p.ID, result.Attestation{
				Kind: gatewaypb.AttestationKindInToto,
				InToto: result.InTotoAttestation{
					PredicateType: slsa.PredicateSLSAProvenance,
				},
				ContentFunc: func() ([]byte, error) {
					end := time.Now()
					pr.Metadata.BuildFinishedOn = &end
					// TODO: pass indent to json.Marshal
					return json.MarshalIndent(pr, "", "  ")
				},
			}, nil)
		}

		return res, nil
	}
}
