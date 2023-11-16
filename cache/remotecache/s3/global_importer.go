package s3

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/buildkit/cache/remotecache"
	v1 "github.com/moby/buildkit/cache/remotecache/v1"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ResolveGlobalCacheImporterFunc can resolve cache from a global s3 cache.
func ResolveGlobalCacheImporterFunc() remotecache.ResolveCacheImporterFunc {
	return func(ctx context.Context, _ session.Group, attrs map[string]string) (remotecache.Importer, ocispecs.Descriptor, error) {
		config, err := getConfig(attrs)
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		s3Client, err := newS3Client(ctx, config)
		if err != nil {
			return nil, ocispecs.Descriptor{}, err
		}
		f := importerFunc(func(ctx context.Context, desc ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error) {
			gi := &globalImporter{
				ctx:      ctx,
				id:       id,
				s3Client: s3Client,
				config:   config,
				worker:   w,
				getByID:  map[string]item{},
			}
			gki := &globalKeyImporter{gi}
			gri := &globalResultImporter{gi}

			return solver.NewCacheManager(ctx, id, gki, gri), nil
		})
		return f, ocispecs.Descriptor{}, nil
	}
}

type importerFunc func(ctx context.Context, desc ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error)

func (f importerFunc) Resolve(ctx context.Context, desc ocispecs.Descriptor, id string, w worker.Worker) (solver.CacheManager, error) {
	return f(ctx, desc, id, w)
}

type globalImporter struct {
	ctx      context.Context
	id       string
	s3Client *s3Client
	config   Config
	worker   worker.Worker

	remotesLock sync.RWMutex
	getByID     map[string]item
}

type item struct {
	createdAt time.Time
	result    *solver.Remote
}

type globalKeyImporter struct {
	*globalImporter
}

func (gki *globalKeyImporter) AddLink(id string, link solver.CacheInfoLink, target string) error {
	return nil // the importer is read only
}

func (gki *globalKeyImporter) AddResult(id string, res solver.CacheResult) error {
	return nil // the importer is read only
}

func (gki *globalKeyImporter) Exists(id string) bool {
	key := gki.s3Client.prefix + manifests + id
	createdAt, _ := gki.s3Client.exists(gki.ctx, key)
	return createdAt != nil
}

func (gki *globalKeyImporter) HasLink(id string, link solver.CacheInfoLink, target string) bool {
	return false // TODO if needed
}

func (gki *globalKeyImporter) Load(id string, resultID string) (solver.CacheResult, error) {
	gki.remotesLock.RLock()
	item, found := gki.getByID[resultID]
	gki.remotesLock.RUnlock()
	if !found {
		return solver.CacheResult{}, errors.WithStack(solver.ErrNotFound)
	}
	return solver.CacheResult{
		ID:        remoteID(item.result),
		CreatedAt: item.createdAt,
	}, nil
}

func (gki *globalKeyImporter) Release(resultID string) error {
	return nil // the importer is read only and this should not happen
}

func (gki *globalKeyImporter) Walk(fn func(id string) error) error {
	return nil // explicitly not listing all possible manifests
}

func (gki *globalKeyImporter) WalkIDsByResult(resultID string, fn func(string) error) error {
	return nil // TODO if needed
}

func (gki *globalKeyImporter) WalkBacklinks(id string, fn func(id string, link solver.CacheInfoLink) error) error {
	if !isCachableKey(id) {
		return nil
	}
	prefix := gki.s3Client.prefix + backlinks + id + separator
	return gki.s3Client.ListAllOjectsV2Prefix(gki.ctx, prefix, func(out *s3.ListObjectsV2Output) (cont bool, err error) {
		for _, obj := range out.Contents {
			parts := strings.Split(strings.TrimPrefix(*obj.Key, prefix), separator)
			if len(parts) != 4 {
				// TODO: log something, but how
				continue
			}
			part1, err := strconv.Atoi(parts[1])
			if err != nil {
				// TODO: log something, but how
				continue
			}
			// `ID::dgst::inputIdx::selector::parent.ID`
			// backlink from parent to id
			fn(parts[3], solver.CacheInfoLink{
				Digest:   digest.Digest(parts[0]),
				Input:    solver.Index(part1),
				Selector: digest.Digest(parts[2]),
			})

		}
		return true, nil
	})
}

func (gki *globalKeyImporter) WalkLinks(id string, link solver.CacheInfoLink, fn func(id string) error) error {
	if !isCachableKey(id) {
		return nil
	}
	recordKey := outputKey(link.Digest, link.Output)
	prefix := vertexPrefix(id, recordKey, int(link.Input), link.Selector)
	prefixKey := gki.s3Client.prefix + links + prefix + separator

	return gki.s3Client.ListAllOjectsV2Prefix(gki.ctx, prefixKey, func(out *s3.ListObjectsV2Output) (cont bool, err error) {
		for _, obj := range out.Contents {
			id := strings.TrimPrefix(*obj.Key, prefixKey)
			err := fn(id)
			if err != nil {
				return false, err
			}
		}
		return true, nil
	})
}

func (gki *globalKeyImporter) WalkResults(id string, fn func(solver.CacheResult) error) error {
	// walk results lists the results ids (or ids of a layers list) of a record
	// step, usually there's only one id but some of the code actually have a
	// list here. Here, we load and memorize them as well, because this probably
	// means we want to use (or Load) them later on.
	//
	// For finalize: a different approach could be to add an intermediary step
	// that would :
	//  - store result id lists instead of mini manifests of layers
	//  - then gki.Load would actually load layer lists from the result ID
	//
	// This would actually create more file so I skipped this.
	// The other cache plugins seem to no really do that as well, but they have
	// a bit less entries, this could lead to memory spikes, but not too badly
	// since those mainly contain blob digests.
	key := gki.s3Client.prefix + manifests + id

	obj, err := gki.s3Client.GetObject(gki.ctx, &s3.GetObjectInput{
		Bucket: &gki.s3Client.bucket,
		Key:    &key,
	})
	if err != nil {
		return err
	}

	var manifest MiniManifest

	err = json.NewDecoder(obj.Body).Decode(&manifest)
	obj.Body.Close()
	if err != nil {
		return err
	}

	if len(manifest.Layers) == 0 {
		return nil
	}

	allLayers := v1.DescriptorProvider{}

	for _, l := range manifest.Layers {
		dpp, err := gki.s3Client.makeDescriptorProviderPair(l)
		if err != nil {
			return err
		}
		allLayers[l.Blob] = *dpp
	}

	remote, err := manifest.getRemoteChain(allLayers)
	if err != nil {
		return err
	}
	remoteID := remoteID(remote)

	gki.remotesLock.Lock()
	gki.globalImporter.getByID[remoteID] = item{
		result:    remote,
		createdAt: *obj.LastModified,
	}
	gki.remotesLock.Unlock()

	return fn(solver.CacheResult{
		ID:        remoteID,
		CreatedAt: manifest.CreatedAt,
	})
}

type globalResultImporter struct {
	*globalImporter
}

func (gri *globalResultImporter) Exists(ctx context.Context, id string) bool {
	gri.remotesLock.RLock()
	_, found := gri.getByID[id]
	gri.remotesLock.RUnlock()
	return found
}

func (gri *globalResultImporter) Load(ctx context.Context, res solver.CacheResult) (solver.Result, error) {
	gri.remotesLock.RLock()
	item, found := gri.getByID[res.ID]
	gri.remotesLock.RUnlock()
	if !found {
		return nil, errors.WithStack(solver.ErrNotFound)
	}
	ref, err := gri.worker.FromRemote(ctx, item.result)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load result from remote")
	}
	return worker.NewWorkerRefResult(ref, gri.worker), nil
}

func (gri *globalResultImporter) LoadRemotes(ctx context.Context, res solver.CacheResult, compressionopts *compression.Config, s session.Group) ([]*solver.Remote, error) {
	gri.remotesLock.RLock()
	item, found := gri.getByID[res.ID]
	gri.remotesLock.RUnlock()
	if !found {
		return nil, errors.WithStack(solver.ErrNotFound)
	}

	if compressionopts == nil {
		return []*solver.Remote{item.result}, nil
	}
	// Any of blobs in the remote must meet the specified compression option.
	match := false
	for _, desc := range item.result.Descriptors {
		m := compression.IsMediaType(compressionopts.Type, desc.MediaType)
		match = match || m
		if compressionopts.Force && !m {
			match = false
			break
		}
	}
	if match {
		return []*solver.Remote{item.result}, nil
	}
	return nil, nil // return nil as it's best effort.
}

func (gri *globalResultImporter) Save(solver.Result, time.Time) (solver.CacheResult, error) {
	return solver.CacheResult{}, errors.Errorf("importer is immutable")
}
