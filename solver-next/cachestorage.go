package solver

import (
	"context"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

var ErrNotFound = errors.Errorf("not found")

// CacheKeyStorage is interface for persisting cache metadata
type CacheKeyStorage interface {
	Get(id string) (CacheKeyInfo, error)
	Walk(fn func(id string) error) error
	Set(info CacheKeyInfo) error

	WalkResults(id string, fn func(CacheResult) error) error
	Load(id string, resultID string) (CacheResult, error)
	AddResult(id string, res CacheResult) error
	Release(resultID string) error

	AddLink(id string, link CacheInfoLink, target string) error
	WalkLinks(id string, link CacheInfoLink, fn func(id string) error) error
}

// CacheKeyInfo is storable metadata about single cache key
type CacheKeyInfo struct {
	ID     string
	Base   digest.Digest
	Output int
	// Deps   []CacheKeyInfoWithSelector
}

// CacheKeyInfoWithSelector is CacheKeyInfo combined with a selector
type CacheKeyInfoWithSelector struct {
	ID       string
	Selector digest.Digest
}

// CacheResult is a record for a single solve result
type CacheResult struct {
	// Payload   []byte
	CreatedAt time.Time
	ID        string
}

// CacheInfoLink is a link between two cache keys
type CacheInfoLink struct {
	Input    Index         `json:"Input,omitempty"`
	Output   Index         `json:"Output,omitempty"`
	Digest   digest.Digest `json:"Digest,omitempty"`
	Selector digest.Digest `json:"Selector,omitempty"`
}

// CacheResultStorage is interface for converting cache metadata result to
// actual solve result
type CacheResultStorage interface {
	Save(Result) (CacheResult, error)
	Load(ctx context.Context, res CacheResult) (Result, error)
	LoadRemote(ctx context.Context, res CacheResult) (*Remote, error)
	Exists(id string) bool
}
