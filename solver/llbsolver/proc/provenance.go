package proc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	slsa "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/exporter/containerimage"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver"
	"github.com/moby/buildkit/solver/llbsolver/provenance"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func ProvenanceProcessor(attrs map[string]string) llbsolver.Processor {
	return func(ctx context.Context, res *llbsolver.Result, s *llbsolver.Solver, j *solver.Job) (*llbsolver.Result, error) {
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
				return nil, errors.Wrapf(err, "failed to parse platforms passed to provenance processor")
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
			case "full":
				mode = "max"
			case "max", "min":
				mode = v
			default:
				return nil, errors.Errorf("invalid mode %q", v)
			}
		}

		for _, p := range ps.Platforms {
			cp, ok := res.Provenance.Refs[p.ID]
			if !ok {
				return nil, errors.New("no build info found for provenance")
			}

			pr, err := provenance.NewPredicate(cp)
			if err != nil {
				return nil, err
			}

			st := j.StartedTime()

			pr.Metadata.BuildStartedOn = &st
			pr.Metadata.Reproducible = reproducible
			pr.Metadata.BuildInvocationID = buildID

			var addLayers func() error

			if mode != "max" {
				args := make(map[string]string)
				for k, v := range pr.Invocation.Parameters.Args {
					if strings.HasPrefix(k, "build-arg:") || strings.HasPrefix(k, "label:") {
						pr.Metadata.Completeness.Parameters = false
						continue
					}
					args[k] = v
				}
				pr.Invocation.Parameters.Args = args
				pr.Invocation.Parameters.Secrets = nil
				pr.Invocation.Parameters.SSH = nil
			} else {
				dgsts, err := provenance.AddBuildConfig(ctx, pr, res.Refs[p.ID])
				if err != nil {
					return nil, err
				}

				r, err := res.Refs[p.ID].Result(ctx)
				if err != nil {
					return nil, err
				}

				addLayers = func() error {
					e := newCacheExporter()
					if _, err := r.CacheKeys()[0].Exporter.ExportTo(ctx, e, solver.CacheExportOpt{
						ResolveRemotes: resolveRemotes,
						Mode:           solver.CacheExportModeRemoteOnly,
						ExportRoots:    true,
					}); err != nil {
						return err
					}

					m := map[string][][]ocispecs.Descriptor{}

					for l, descs := range e.layers {
						idx, ok := dgsts[l.digest]
						if !ok {
							continue
						}

						m[fmt.Sprintf("step%d:%d", idx, l.index)] = descs
					}

					if len(m) != 0 {
						if pr.Metadata == nil {
							pr.Metadata = &provenance.ProvenanceMetadata{}
						}

						pr.Metadata.BuildKitMetadata.Layers = m
					}

					return nil
				}
			}

			res.AddAttestation(p.ID, result.Attestation{
				Kind: gatewaypb.AttestationKindInToto,
				Metadata: map[string][]byte{
					result.AttestationReasonKey:     result.AttestationReasonProvenance,
				},
				InToto: result.InTotoAttestation{
					PredicateType: slsa.PredicateSLSAProvenance,
				},
				Path: "provenance.json",
				ContentFunc: func() ([]byte, error) {
					end := time.Now()
					pr.Metadata.BuildFinishedOn = &end

					if addLayers != nil {
						if err := addLayers(); err != nil {
							return nil, err
						}
					}

					// TODO: pass indent to json.Marshal
					return json.MarshalIndent(pr, "", "  ")
				},
			}, nil)
		}

		return res, nil
	}
}

func resolveRemotes(ctx context.Context, res solver.Result) ([]*solver.Remote, error) {
	ref, ok := res.Sys().(*worker.WorkerRef)
	if !ok {
		return nil, errors.Errorf("invalid result: %T", res.Sys())
	}

	remotes, err := ref.GetRemotes(ctx, false, config.RefConfig{}, true, nil)
	if err != nil {
		if errors.Is(err, cache.ErrNoBlobs) {
			return nil, nil
		}
		return nil, err
	}
	return remotes, nil
}

type edge struct {
	digest digest.Digest
	index  int
}

func newCacheExporter() *cacheExporter {
	return &cacheExporter{
		m:      map[interface{}]struct{}{},
		layers: map[edge][][]ocispecs.Descriptor{},
	}
}

type cacheExporter struct {
	layers map[edge][][]ocispecs.Descriptor
	m      map[interface{}]struct{}
}

func (ce *cacheExporter) Add(dgst digest.Digest) solver.CacheExporterRecord {
	return &cacheRecord{
		ce: ce,
	}
}

func (ce *cacheExporter) Visit(v interface{}) {
	ce.m[v] = struct{}{}
}

func (ce *cacheExporter) Visited(v interface{}) bool {
	_, ok := ce.m[v]
	return ok
}

type cacheRecord struct {
	ce *cacheExporter
}

func (c *cacheRecord) AddResult(dgst digest.Digest, idx int, createdAt time.Time, result *solver.Remote) {
	if result == nil || dgst == "" {
		return
	}
	e := edge{
		digest: dgst,
		index:  idx,
	}
	descs := make([]ocispecs.Descriptor, len(result.Descriptors))
	for i, desc := range result.Descriptors {
		d := desc
		containerimage.RemoveInternalLayerAnnotations(&d, true)
		descs[i] = d
	}
	c.ce.layers[e] = append(c.ce.layers[e], descs)
}

func (c *cacheRecord) LinkFrom(rec solver.CacheExporterRecord, index int, selector string) {
}
