package containerblob

import (
	"strings"

	"github.com/containerd/containerd/v2/pkg/reference"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/solver/llbsolver/provenance"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/moby/buildkit/source"
	srctypes "github.com/moby/buildkit/source/types"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type ImageBlobIdentifier struct {
	Reference  reference.Spec
	RecordType client.UsageRecordType
	Filename   string
	Perm       int
	UID        int
	GID        int
}

func NewImageBlobIdentifier(str string) (*ImageBlobIdentifier, error) {
	ref, err := reference.Parse(str)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if ref.Object == "" {
		return nil, errors.WithStack(reference.ErrObjectRequired)
	}
	return &ImageBlobIdentifier{Reference: ref}, nil
}

var _ source.Identifier = (*ImageBlobIdentifier)(nil)

func (*ImageBlobIdentifier) Scheme() string {
	return srctypes.DockerImageBlobScheme
}

func (id *ImageBlobIdentifier) Capture(c *provenance.Capture, pin string) error {
	dgst, err := digest.Parse(pin)
	if err != nil {
		return errors.Wrapf(err, "failed to parse image digest %s", pin)
	}

	if !strings.HasPrefix(id.Reference.Object, "@") {
		return errors.Errorf("invalid image blob reference %s", id.Reference.String())
	}

	dgst2, err := digest.Parse(id.Reference.Object[1:])
	if err != nil {
		return errors.Wrapf(err, "failed to parse image digest %s", pin)
	}

	if dgst != dgst2 {
		return errors.Errorf("invalid image blob reference: digest mismatch %s != %s", dgst, dgst2)
	}

	c.AddImageBlob(provenancetypes.ImageBlobSource{
		Ref:    id.Reference.String(),
		Digest: dgst,
	})
	return nil
}
