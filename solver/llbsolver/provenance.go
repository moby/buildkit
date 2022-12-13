package llbsolver

import (
	"context"
	"sync"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver/ops"
	"github.com/moby/buildkit/solver/llbsolver/provenance"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type resultWithBridge struct {
	res    *frontend.Result
	bridge *provenanceBridge
}

// provenanceBridge provides scoped access to LLBBridge and captures the request it makes for provenance
type provenanceBridge struct {
	*llbBridge
	mu  sync.Mutex
	req *frontend.SolveRequest

	images     []provenance.ImageSource
	builds     []resultWithBridge
	subBridges []*provenanceBridge
}

func (b *provenanceBridge) eachRef(f func(r solver.ResultProxy) error) error {
	for _, b := range b.builds {
		if err := b.res.EachRef(f); err != nil {
			return err
		}
	}
	for _, b := range b.subBridges {
		if err := b.eachRef(f); err != nil {
			return err
		}
	}
	return nil
}

func (b *provenanceBridge) allImages() []provenance.ImageSource {
	res := make([]provenance.ImageSource, 0, len(b.images))
	res = append(res, b.images...)
	for _, sb := range b.subBridges {
		res = append(res, sb.allImages()...)
	}
	return res
}

func (b *provenanceBridge) requests(r *frontend.Result) (*resultRequests, error) {
	reqs := &resultRequests{
		refs: make(map[string]*resultWithBridge),
		atts: make(map[string][]*resultWithBridge),
	}

	if r.Ref != nil {
		ref, ok := b.findByResult(r.Ref)
		if !ok {
			return nil, errors.Errorf("could not find request for ref %s", r.Ref.ID())
		}
		reqs.ref = ref
	}

	for k, ref := range r.Refs {
		r, ok := b.findByResult(ref)
		if !ok {
			return nil, errors.Errorf("could not find request for ref %s", ref.ID())
		}
		reqs.refs[k] = r
	}

	for k, atts := range r.Attestations {
		for _, att := range atts {
			if att.Ref == nil {
				continue
			}
			r, ok := b.findByResult(att.Ref)
			if !ok {
				return nil, errors.Errorf("could not find request for ref %s", att.Ref.ID())
			}
			reqs.atts[k] = append(reqs.atts[k], r)
		}
	}

	ps, err := exptypes.ParsePlatforms(r.Metadata)
	if err != nil {
		return nil, err
	}
	reqs.platforms = ps.Platforms

	return reqs, nil
}

func (b *provenanceBridge) findByResult(rp solver.ResultProxy) (*resultWithBridge, bool) {
	for _, br := range b.subBridges {
		if req, ok := br.findByResult(rp); ok {
			return req, true
		}
	}
	for _, bld := range b.builds {
		found := false
		bld.res.EachRef(func(r solver.ResultProxy) error {
			if r.ID() == rp.ID() {
				found = true
			}
			return nil
		})
		if found {
			return &bld, true
		}
	}
	return nil, false
}

func (b *provenanceBridge) ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (dgst digest.Digest, config []byte, err error) {
	dgst, config, err = b.llbBridge.ResolveImageConfig(ctx, ref, opt)
	if err != nil {
		return "", nil, err
	}

	b.images = append(b.images, provenance.ImageSource{
		Ref:      ref,
		Platform: opt.Platform,
		Digest:   dgst,
	})
	return dgst, config, nil
}

func (b *provenanceBridge) Solve(ctx context.Context, req frontend.SolveRequest, sid string) (res *frontend.Result, err error) {
	if req.Definition != nil && req.Definition.Def != nil && req.Frontend != "" {
		return nil, errors.New("cannot solve with both Definition and Frontend specified")
	}

	if req.Definition != nil && req.Definition.Def != nil {
		rp := newResultProxy(b, req)
		res = &frontend.Result{Ref: rp}
		b.mu.Lock()
		b.builds = append(b.builds, resultWithBridge{res: res, bridge: b})
		b.mu.Unlock()
	} else if req.Frontend != "" {
		f, ok := b.llbBridge.frontends[req.Frontend]
		if !ok {
			return nil, errors.Errorf("invalid frontend: %s", req.Frontend)
		}
		wb := &provenanceBridge{llbBridge: b.llbBridge, req: &req}
		res, err = f.Solve(ctx, wb, req.FrontendOpt, req.FrontendInputs, sid, b.llbBridge.sm)
		if err != nil {
			return nil, err
		}
		wb.builds = append(wb.builds, resultWithBridge{res: res, bridge: wb})
		b.mu.Lock()
		b.subBridges = append(b.subBridges, wb)
		b.mu.Unlock()
	} else {
		return &frontend.Result{}, nil
	}
	if req.Evaluate {
		err = res.EachRef(func(ref solver.ResultProxy) error {
			_, err := res.Ref.Result(ctx)
			return err
		})
	}
	return
}

type resultRequests struct {
	ref       *resultWithBridge
	refs      map[string]*resultWithBridge
	atts      map[string][]*resultWithBridge
	platforms []exptypes.Platform
}

// filterImagePlatforms filter out images that not for the current platform if an image existist for every platform in a result
func (reqs *resultRequests) filterImagePlatforms(k string, imgs []provenance.ImageSource) []provenance.ImageSource {
	if len(reqs.platforms) == 0 {
		return imgs
	}
	m := map[string]string{}
	for _, img := range imgs {
		if _, ok := m[img.Ref]; ok {
			continue
		}
		hasPlatform := true
		for _, p := range reqs.platforms {
			matcher := platforms.NewMatcher(p.Platform)
			found := false
			for _, img2 := range imgs {
				if img.Ref == img2.Ref && img2.Platform != nil {
					if matcher.Match(*img2.Platform) {
						found = true
						break
					}
				}
			}
			if !found {
				hasPlatform = false
				break
			}
		}
		if hasPlatform {
			m[img.Ref] = img.Ref
		}
	}

	var current ocispecs.Platform
	for _, p := range reqs.platforms {
		if p.ID == k {
			current = p.Platform
		}
	}

	out := make([]provenance.ImageSource, 0, len(imgs))
	for _, img := range imgs {
		if _, ok := m[img.Ref]; ok && img.Platform != nil {
			if current.OS == img.Platform.OS && current.Architecture == img.Platform.Architecture {
				out = append(out, img)
			}
		} else {
			out = append(out, img)
		}
	}
	return out
}

func (reqs *resultRequests) allRes() map[string]struct{} {
	res := make(map[string]struct{})
	if reqs.ref != nil {
		res[reqs.ref.res.Ref.ID()] = struct{}{}
	}
	for _, r := range reqs.refs {
		res[r.res.Ref.ID()] = struct{}{}
	}
	for _, rs := range reqs.atts {
		for _, r := range rs {
			res[r.res.Ref.ID()] = struct{}{}
		}
	}
	return res
}

func captureProvenance(ctx context.Context, res solver.CachedResultWithProvenance) (*provenance.Capture, error) {
	if res == nil {
		return nil, nil
	}
	c := &provenance.Capture{}

	err := res.WalkProvenance(ctx, func(pp solver.ProvenanceProvider) error {
		switch op := pp.(type) {
		case *ops.SourceOp:
			id, pin := op.Pin()
			switch s := id.(type) {
			case *source.ImageIdentifier:
				dgst, err := digest.Parse(pin)
				if err != nil {
					return errors.Wrapf(err, "failed to parse image digest %s", pin)
				}
				c.AddImage(provenance.ImageSource{
					Ref:      s.Reference.String(),
					Platform: s.Platform,
					Digest:   dgst,
				})
			case *source.LocalIdentifier:
				c.AddLocal(provenance.LocalSource{
					Name: s.Name,
				})
			case *source.GitIdentifier:
				url := s.Remote
				if s.Ref != "" {
					url += "#" + s.Ref
				}
				c.AddGit(provenance.GitSource{
					URL:    url,
					Commit: pin,
				})
				if s.AuthTokenSecret != "" {
					c.AddSecret(provenance.Secret{
						ID:       s.AuthTokenSecret,
						Optional: true,
					})
				}
				if s.AuthHeaderSecret != "" {
					c.AddSecret(provenance.Secret{
						ID:       s.AuthHeaderSecret,
						Optional: true,
					})
				}
				if s.MountSSHSock != "" {
					c.AddSSH(provenance.SSH{
						ID:       s.MountSSHSock,
						Optional: true,
					})
				}
			case *source.HTTPIdentifier:
				dgst, err := digest.Parse(pin)
				if err != nil {
					return errors.Wrapf(err, "failed to parse HTTP digest %s", pin)
				}
				c.AddHTTP(provenance.HTTPSource{
					URL:    s.URL,
					Digest: dgst,
				})
			case *source.OCIIdentifier:
				dgst, err := digest.Parse(pin)
				if err != nil {
					return errors.Wrapf(err, "failed to parse OCI digest %s", pin)
				}
				c.AddLocalImage(provenance.ImageSource{
					Ref:      s.Name,
					Platform: s.Platform,
					Digest:   dgst,
				})
			default:
				return errors.Errorf("unknown source identifier %T", id)
			}
		case *ops.ExecOp:
			pr := op.Proto()
			for _, m := range pr.Mounts {
				if m.MountType == pb.MountType_SECRET {
					c.AddSecret(provenance.Secret{
						ID:       m.SecretOpt.GetID(),
						Optional: m.SecretOpt.GetOptional(),
					})
				}
				if m.MountType == pb.MountType_SSH {
					c.AddSSH(provenance.SSH{
						ID:       m.SSHOpt.GetID(),
						Optional: m.SSHOpt.GetOptional(),
					})
				}
			}
			for _, se := range pr.Secretenv {
				c.AddSecret(provenance.Secret{
					ID:       se.GetID(),
					Optional: se.GetOptional(),
				})
			}
			if pr.Network != pb.NetMode_NONE {
				c.NetworkAccess = true
			}
		case *ops.BuildOp:
			c.IncompleteMaterials = true // not supported yet
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, err
}
