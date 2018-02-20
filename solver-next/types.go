package solver

import (
	"context"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Vertex is one node in the build graph
type Vertex interface {
	// Digest is a content-addressable vertex identifier
	Digest() digest.Digest
	// Sys returns an internal value that is used to execute the vertex. Usually
	// this is capured by the operation resolver method during solve.
	Sys() interface{}
	Options() VertexOptions
	// Array of edges current vertex depends on.
	Inputs() []Edge
	Name() string
}

// Index is a index value for output edge
type Index int

// Edge is a path to a specific output of the vertex
type Edge struct {
	Index  Index
	Vertex Vertex
}

// VertexOptions has optional metadata for the vertex that is not contained in digest
type VertexOptions struct {
	IgnoreCache bool
	CacheSource CacheManager
	Description map[string]string // text values with no special meaning for solver
	// WorkerConstraint
}

// Result is an abstract return value for a solve
type Result interface {
	ID() string
	Release(context.Context) error
	Sys() interface{}
}

// CachedResult is a result connected with its cache key
type CachedResult interface {
	Result
	CacheKey() ExportableCacheKey
	Export(ctx context.Context, converter func(context.Context, Result) (*Remote, error)) ([]ExportRecord, error)
}

// Exporter can export the artifacts of the build chain
type Exporter interface {
	Export(ctx context.Context, m map[digest.Digest]*ExportRecord, converter func(context.Context, Result) (*Remote, error)) (*ExportRecord, error)
}

// ExportRecord defines a single record in the exported cache chain
type ExportRecord struct {
	Digest digest.Digest
	Links  map[CacheLink]struct{}
	Remote *Remote
}

// Remote is a descriptor or a list of stacked descriptors that can be pulled
// from a content provider
type Remote struct {
	Descriptors []ocispec.Descriptor
	Provider    content.Provider
}

// CacheLink is a link between two cache records
type CacheLink struct {
	Source   digest.Digest
	Input    Index
	Output   Index
	Base     digest.Digest
	Selector digest.Digest
}

// Op is an implementation for running a vertex
type Op interface {
	// CacheMap returns structure describing how the operation is cached
	CacheMap(context.Context) (*CacheMap, error)
	// Exec runs an operation given results from previous operations.
	Exec(ctx context.Context, inputs []Result) (outputs []Result, err error)
}

type ResultBasedCacheFunc func(context.Context, Result) (digest.Digest, error)

type CacheMap struct {
	// Digest is a base digest for operation that needs to be combined with
	// inputs cache or selectors for dependencies.
	Digest digest.Digest
	Deps   []struct {
		// Optional digest that is merged with the cache key of the input
		Selector digest.Digest
		// Optional function that returns a digest for the input based on its
		// return value
		ComputeDigestFunc ResultBasedCacheFunc
	}
}

// ExportableCacheKey is a cache key connected with an exporter that can export
// a chain of cacherecords pointing to that key
type ExportableCacheKey struct {
	CacheKey
	Exporter
}

// CacheKey is an identifier for storing/loading build cache
type CacheKey interface {
	// Deps are dependant cache keys
	Deps() []CacheKeyWithSelector
	// Base digest for operation. Usually CacheMap.Digest
	Digest() digest.Digest
	// Index for the output that is cached
	Output() Index
	// Helpers for implementations for adding internal metadata
	SetValue(key, value interface{})
	GetValue(key interface{}) interface{}
}

// CacheRecord is an identifier for loading in cache
type CacheRecord struct {
	ID           string
	CacheKey     ExportableCacheKey
	CacheManager CacheManager
	// Loadable bool
	// Size int
	CreatedAt time.Time
	Priority  int
}

// CacheManager implements build cache backend
type CacheManager interface {
	// ID is used to identify cache providers that are backed by same source
	// to avoid duplicate calls to the same provider
	ID() string
	// Query searches for cache paths from one cache key to the output of a
	// possible match.
	Query(inp []ExportableCacheKey, inputIndex Index, dgst digest.Digest, outputIndex Index, selector digest.Digest) ([]*CacheRecord, error)
	// Load pulls and returns the cached result
	Load(ctx context.Context, rec *CacheRecord) (Result, error)
	// Save saves a result based on a cache key
	Save(key CacheKey, s Result) (ExportableCacheKey, error)
}

// NewCacheKey creates a new cache key for a specific output index
func NewCacheKey(dgst digest.Digest, index Index, deps []CacheKeyWithSelector) CacheKey {
	return &cacheKey{
		dgst:   dgst,
		deps:   deps,
		index:  index,
		values: &sync.Map{},
	}
}

// CacheKeyWithSelector combines a cache key with an optional selector digest.
// Used to limit the matches for dependency cache key.
type CacheKeyWithSelector struct {
	Selector digest.Digest
	CacheKey ExportableCacheKey
}

type cacheKey struct {
	dgst   digest.Digest
	index  Index
	deps   []CacheKeyWithSelector
	values *sync.Map
}

func (ck *cacheKey) SetValue(key, value interface{}) {
	ck.values.Store(key, value)
}

func (ck *cacheKey) GetValue(key interface{}) interface{} {
	v, _ := ck.values.Load(key)
	return v
}

func (ck *cacheKey) Deps() []CacheKeyWithSelector {
	return ck.deps
}

func (ck *cacheKey) Digest() digest.Digest {
	return ck.dgst
}

func (ck *cacheKey) Output() Index {
	return ck.index
}

func (ck *cacheKey) Export(ctx context.Context, converter func(context.Context, Result) ([]ocispec.Descriptor, content.Provider, error)) ([]ExportRecord, content.Provider, error) {
	return nil, nil, nil
}
