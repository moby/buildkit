package containerblob

import (
	"context"
	"strconv"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	srctypes "github.com/moby/buildkit/source/types"
	"github.com/pkg/errors"
)

type SourceOpt struct {
	ContentStore  content.Store
	CacheAccessor cache.Accessor
	RegistryHosts docker.RegistryHosts
}

type Source struct {
	SourceOpt
}

var _ source.Source = &Source{}

func NewSource(opt SourceOpt) (*Source, error) {
	is := &Source{
		SourceOpt: opt,
	}

	return is, nil
}

func (is *Source) Schemes() []string {
	return []string{srctypes.DockerImageBlobScheme}
}

func (is *Source) Identifier(scheme, ref string, attrs map[string]string, platform *pb.Platform) (source.Identifier, error) {
	return is.registryIdentifier(ref, attrs, platform)
}

func (is *Source) Resolve(ctx context.Context, id source.Identifier, sm *session.Manager, vtx solver.Vertex) (source.SourceInstance, error) {
	imageIdentifier, ok := id.(*ImageBlobIdentifier)
	if !ok {
		return nil, errors.Errorf("invalid image blob identifier %v", id)
	}
	return &puller{
		src:            is,
		id:             imageIdentifier,
		SessionManager: sm,
	}, nil
}

func (is *Source) registryIdentifier(ref string, attrs map[string]string, platform *pb.Platform) (source.Identifier, error) {
	id, err := NewImageBlobIdentifier(ref)
	if err != nil {
		return nil, err
	}

	for k, v := range attrs {
		switch k {
		case pb.AttrHTTPFilename:
			id.Filename = v
		case pb.AttrHTTPPerm:
			i, err := strconv.ParseInt(v, 0, 64)
			if err != nil {
				return nil, err
			}
			id.Perm = int(i)
		case pb.AttrHTTPUID:
			i, err := strconv.ParseInt(v, 0, 64)
			if err != nil {
				return nil, err
			}
			id.UID = int(i)
		case pb.AttrHTTPGID:
			i, err := strconv.ParseInt(v, 0, 64)
			if err != nil {
				return nil, err
			}
			id.GID = int(i)
		case pb.AttrImageRecordType:
			rt, err := parseImageRecordType(v)
			if err != nil {
				return nil, err
			}
			id.RecordType = rt
		}
	}

	return id, nil
}

func parseImageRecordType(v string) (client.UsageRecordType, error) {
	switch client.UsageRecordType(v) {
	case "", client.UsageRecordTypeRegular:
		return client.UsageRecordTypeRegular, nil
	case client.UsageRecordTypeInternal:
		return client.UsageRecordTypeInternal, nil
	case client.UsageRecordTypeFrontend:
		return client.UsageRecordTypeFrontend, nil
	default:
		return "", errors.Errorf("invalid record type %s", v)
	}
}
