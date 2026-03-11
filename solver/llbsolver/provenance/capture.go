package provenance

import (
	"cmp"
	"maps"
	"slices"

	distreference "github.com/distribution/reference"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/urlutil"
	digest "github.com/opencontainers/go-digest"
)

type Result = result.Result[*Capture]

type Capture struct {
	Frontend            string
	Args                map[string]string
	Postprocess         []provenancetypes.Parameters
	Sources             provenancetypes.Sources
	Secrets             []provenancetypes.Secret
	SSH                 []provenancetypes.SSH
	NetworkAccess       bool
	IncompleteMaterials bool
	Samples             map[digest.Digest]*resourcestypes.Samples
}

func (c *Capture) Merge(c2 *Capture) error {
	if c2 == nil {
		return nil
	}
	for _, i := range c2.Sources.Images {
		c.AddImage(i)
	}
	for _, i := range c2.Sources.ImageBlobs {
		c.AddImageBlob(i)
	}
	for _, l := range c2.Sources.Local {
		c.AddLocal(l)
	}
	for _, g := range c2.Sources.Git {
		c.AddGit(g)
	}
	for _, h := range c2.Sources.HTTP {
		c.AddHTTP(h)
	}
	for _, s := range c2.Secrets {
		c.AddSecret(s)
	}
	for _, s := range c2.SSH {
		c.AddSSH(s)
	}
	for _, p := range c2.Postprocess {
		c.AddPostprocess(p)
	}
	if c2.NetworkAccess {
		c.NetworkAccess = true
	}
	if c2.IncompleteMaterials {
		c.IncompleteMaterials = true
	}
	return nil
}

func (c *Capture) Sort() {
	slices.SortFunc(c.Sources.Images, func(a, b provenancetypes.ImageSource) int {
		return cmp.Compare(a.Ref, b.Ref)
	})
	slices.SortFunc(c.Sources.ImageBlobs, func(a, b provenancetypes.ImageBlobSource) int {
		return cmp.Compare(a.Ref, b.Ref)
	})
	slices.SortFunc(c.Sources.Local, func(a, b provenancetypes.LocalSource) int {
		return cmp.Compare(a.Name, b.Name)
	})
	slices.SortFunc(c.Sources.Git, func(a, b provenancetypes.GitSource) int {
		return cmp.Compare(a.URL, b.URL)
	})
	slices.SortFunc(c.Sources.HTTP, func(a, b provenancetypes.HTTPSource) int {
		return cmp.Compare(a.URL, b.URL)
	})
	slices.SortFunc(c.Secrets, func(a, b provenancetypes.Secret) int {
		return cmp.Compare(a.ID, b.ID)
	})
	slices.SortFunc(c.SSH, func(a, b provenancetypes.SSH) int {
		return cmp.Compare(a.ID, b.ID)
	})
}

// OptimizeImageSources filters out image sources by digest reference if same digest
// is already present by a tag reference.
func (c *Capture) OptimizeImageSources() error {
	m := map[string]struct{}{}
	for _, i := range c.Sources.Images {
		ref, nameTag, err := parseRefName(i.Ref)
		if err != nil {
			return err
		}
		if _, ok := ref.(distreference.Canonical); !ok {
			m[nameTag] = struct{}{}
		}
	}

	images := make([]provenancetypes.ImageSource, 0, len(c.Sources.Images))
	for _, i := range c.Sources.Images {
		ref, nameTag, err := parseRefName(i.Ref)
		if err != nil {
			return err
		}
		if _, ok := ref.(distreference.Canonical); ok {
			if _, ok := m[nameTag]; ok {
				continue
			}
		}
		images = append(images, i)
	}
	c.Sources.Images = images
	return nil
}

func (c *Capture) AddImage(i provenancetypes.ImageSource) {
	for _, v := range c.Sources.Images {
		if v.Ref == i.Ref && v.Local == i.Local {
			if v.Platform == i.Platform {
				return
			}
			if v.Platform != nil && i.Platform != nil {
				// NOTE: Deliberately excluding OSFeatures, as there's no extant (or rational) case where a source image is an index and contains images distinguished only by OSFeature
				// See https://github.com/moby/buildkit/pull/4387#discussion_r1376234241 and https://github.com/opencontainers/image-spec/issues/1147
				if v.Platform.Architecture == i.Platform.Architecture && v.Platform.OS == i.Platform.OS && v.Platform.OSVersion == i.Platform.OSVersion && v.Platform.Variant == i.Platform.Variant {
					return
				}
			}
		}
	}
	c.Sources.Images = append(c.Sources.Images, i)
}

func (c *Capture) AddImageBlob(i provenancetypes.ImageBlobSource) {
	for _, v := range c.Sources.ImageBlobs {
		if v.Ref == i.Ref && v.Local == i.Local {
			return
		}
	}
	c.Sources.ImageBlobs = append(c.Sources.ImageBlobs, i)
}

func (c *Capture) AddLocal(l provenancetypes.LocalSource) {
	for _, v := range c.Sources.Local {
		if v.Name == l.Name {
			return
		}
	}
	c.Sources.Local = append(c.Sources.Local, l)
}

func (c *Capture) AddGit(g provenancetypes.GitSource) {
	g.URL = urlutil.RedactCredentials(g.URL)
	for _, v := range c.Sources.Git {
		if v.URL == g.URL {
			return
		}
	}
	c.Sources.Git = append(c.Sources.Git, g)
}

func (c *Capture) AddHTTP(h provenancetypes.HTTPSource) {
	h.URL = urlutil.RedactCredentials(h.URL)
	for _, v := range c.Sources.HTTP {
		if v.URL == h.URL {
			return
		}
	}
	c.Sources.HTTP = append(c.Sources.HTTP, h)
}

func (c *Capture) AddSecret(s provenancetypes.Secret) {
	for i, v := range c.Secrets {
		if v.ID == s.ID {
			if !s.Optional {
				c.Secrets[i].Optional = false
			}
			return
		}
	}
	c.Secrets = append(c.Secrets, s)
}

func (c *Capture) AddSSH(s provenancetypes.SSH) {
	if s.ID == "" {
		s.ID = "default"
	}
	for i, v := range c.SSH {
		if v.ID == s.ID {
			if !s.Optional {
				c.SSH[i].Optional = false
			}
			return
		}
	}
	c.SSH = append(c.SSH, s)
}

func (c *Capture) Parameters() provenancetypes.Parameters {
	p := provenancetypes.Parameters{
		Frontend: c.Frontend,
		Args:     maps.Clone(c.Args),
	}
	for _, s := range c.Secrets {
		s2 := s
		p.Secrets = append(p.Secrets, &s2)
	}
	for _, s := range c.SSH {
		s2 := s
		p.SSH = append(p.SSH, &s2)
	}
	for _, s := range c.Sources.Local {
		s2 := s
		p.Locals = append(p.Locals, &s2)
	}
	return p
}

func (c *Capture) AddPostprocess(p provenancetypes.Parameters) {
	if p.Frontend == "" {
		return
	}
	for _, existing := range c.Postprocess {
		if equalParameters(existing, p) {
			return
		}
	}
	c.Postprocess = append(c.Postprocess, cloneParameters(p))
}

func (c *Capture) AddSamples(dgst digest.Digest, samples *resourcestypes.Samples) {
	if c.Samples == nil {
		c.Samples = map[digest.Digest]*resourcestypes.Samples{}
	}
	c.Samples[dgst] = samples
}

func cloneParameters(p provenancetypes.Parameters) provenancetypes.Parameters {
	out := provenancetypes.Parameters{
		Frontend: p.Frontend,
		Args:     maps.Clone(p.Args),
	}
	for _, s := range p.Secrets {
		if s == nil {
			out.Secrets = append(out.Secrets, nil)
			continue
		}
		s2 := *s
		out.Secrets = append(out.Secrets, &s2)
	}
	for _, s := range p.SSH {
		if s == nil {
			out.SSH = append(out.SSH, nil)
			continue
		}
		s2 := *s
		out.SSH = append(out.SSH, &s2)
	}
	for _, l := range p.Locals {
		if l == nil {
			out.Locals = append(out.Locals, nil)
			continue
		}
		l2 := *l
		out.Locals = append(out.Locals, &l2)
	}
	return out
}

func equalParameters(a, b provenancetypes.Parameters) bool {
	if a.Frontend != b.Frontend || !maps.Equal(a.Args, b.Args) {
		return false
	}
	if len(a.Secrets) != len(b.Secrets) || len(a.SSH) != len(b.SSH) || len(a.Locals) != len(b.Locals) {
		return false
	}
	for i := range a.Secrets {
		if !equalSecret(a.Secrets[i], b.Secrets[i]) {
			return false
		}
	}
	for i := range a.SSH {
		if !equalSSH(a.SSH[i], b.SSH[i]) {
			return false
		}
	}
	for i := range a.Locals {
		if !equalLocal(a.Locals[i], b.Locals[i]) {
			return false
		}
	}
	return true
}

func equalSecret(a, b *provenancetypes.Secret) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID && a.Optional == b.Optional
}

func equalSSH(a, b *provenancetypes.SSH) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID && a.Optional == b.Optional
}

func equalLocal(a, b *provenancetypes.LocalSource) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name
}

func parseRefName(s string) (distreference.Named, string, error) {
	ref, err := distreference.ParseNormalizedNamed(s)
	if err != nil {
		return nil, "", err
	}
	name := ref.Name()
	tag := "latest"
	if r, ok := ref.(distreference.Tagged); ok {
		tag = r.Tag()
	}
	return ref, name + ":" + tag, nil
}
