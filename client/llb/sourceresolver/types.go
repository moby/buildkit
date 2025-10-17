package sourceresolver

import (
	"context"
	"time"

	"github.com/moby/buildkit/solver/pb"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type ResolverType int

const (
	ResolverTypeRegistry ResolverType = iota
	ResolverTypeOCILayout
)

type MetaResolver interface {
	ResolveSourceMetadata(ctx context.Context, op *pb.SourceOp, opt Opt) (*MetaResponse, error)
}

type Opt struct {
	LogName        string
	SourcePolicies []*spb.Policy
	Platform       *ocispecs.Platform

	ImageOpt     *ResolveImageOpt
	OCILayoutOpt *ResolveOCILayoutOpt
	GitOpt       *ResolveGitOpt
}

type MetaResponse struct {
	Op *pb.SourceOp

	Image *ResolveImageResponse
	Git   *ResolveGitResponse
	HTTP  *ResolveHTTPResponse
}

type ResolveImageOpt struct {
	ResolveMode string
}

type ResolveImageResponse struct {
	Digest digest.Digest
	Config []byte
}

type ResolveGitOpt struct {
	ReturnObject bool
}

type ResolveGitResponse struct {
	Checksum       string
	Ref            string
	CommitChecksum string
	CommitObject   []byte
	TagObject      []byte
}

type ResolveHTTPResponse struct {
	Digest       digest.Digest
	Filename     string
	LastModified *time.Time
}

type ResolveOCILayoutOpt struct {
	Store ResolveImageConfigOptStore
}

type ResolveImageConfigOptStore struct {
	SessionID string
	StoreID   string
}
