package cache

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/docker/docker/pkg/idtools"
	"github.com/hashicorp/go-multierror"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/winlayers"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// Ref is a reference to cacheable objects.
type Ref interface {
	Mountable
	RefMetadata
	Release(context.Context) error
	IdentityMapping() *idtools.IdentityMapping
	DescHandler(digest.Digest) *DescHandler
}

type ImmutableRef interface {
	Ref
	Clone() ImmutableRef

	Extract(ctx context.Context, s session.Group) error // +progress
	GetRemote(ctx context.Context, createIfNeeded bool, compressionType compression.Type, forceCompression bool, s session.Group) (*solver.Remote, error)
}

type MutableRef interface {
	Ref
	Commit(context.Context) (ImmutableRef, error)
}

type Mountable interface {
	Mount(ctx context.Context, readonly bool, s session.Group) (snapshot.Mountable, error)
}

type ref interface {
	shouldUpdateLastUsed() bool
}

type cacheRecord struct {
	cm *cacheManager
	mu *sync.Mutex // the mutex is shared by records sharing data

	mutable bool
	refs    map[ref]struct{}
	parentRefs
	*cacheMetadata

	// dead means record is marked as deleted
	dead bool

	mountCache snapshot.Mountable

	sizeG flightcontrol.Group

	// these are filled if multiple refs point to same data
	equalMutable   *mutableRef
	equalImmutable *immutableRef

	layerDigestChainCache []digest.Digest
}

// hold ref lock before calling
func (cr *cacheRecord) ref(triggerLastUsed bool, descHandlers DescHandlers) *immutableRef {
	ref := &immutableRef{
		cacheRecord:     cr,
		triggerLastUsed: triggerLastUsed,
		descHandlers:    descHandlers,
	}
	cr.refs[ref] = struct{}{}
	return ref
}

// hold ref lock before calling
func (cr *cacheRecord) mref(triggerLastUsed bool, descHandlers DescHandlers) *mutableRef {
	ref := &mutableRef{
		cacheRecord:     cr,
		triggerLastUsed: triggerLastUsed,
		descHandlers:    descHandlers,
	}
	cr.refs[ref] = struct{}{}
	return ref
}

type parentKind int

const (
	None parentKind = iota
	Layer
	Merge
)

// parentRefs is a disjoint union type that holds either a single layerParent for this record, a list
// of parents if this is a merged record or all nil fields if this record has no parents. At most one
// field should be non-nil at a time.
type parentRefs struct {
	layerParent  *immutableRef
	mergeParents []*immutableRef
}

func (p parentRefs) parentKind() parentKind {
	if len(p.mergeParents) > 0 {
		return Merge
	}
	if p.layerParent != nil {
		return Layer
	}
	return None
}

// caller must hold cacheManager.mu
func (p parentRefs) release(ctx context.Context) (rerr error) {
	switch p.parentKind() {
	case Layer:
		p.layerParent.mu.Lock()
		defer p.layerParent.mu.Unlock()
		return p.layerParent.release(ctx)
	case Merge:
		for i, parent := range p.mergeParents {
			if parent == nil {
				continue
			}
			parent.mu.Lock()
			if err := parent.release(ctx); err != nil {
				rerr = multierror.Append(rerr, err).ErrorOrNil()
			} else {
				p.mergeParents[i] = nil
			}
			parent.mu.Unlock()
		}
	}
	return rerr
}

func (p parentRefs) clone() parentRefs {
	switch p.parentKind() {
	case Layer:
		p.layerParent = p.layerParent.clone()
	case Merge:
		newParents := make([]*immutableRef, len(p.mergeParents))
		for i, p := range p.mergeParents {
			newParents[i] = p.clone()
		}
		p.mergeParents = newParents
	}
	return p
}

func (p parentRefs) parentSnapshotIDs() []string {
	switch p.parentKind() {
	case Layer:
		return []string{p.layerParent.getSnapshotID()}
	case Merge:
		snapshots := make([]string, len(p.mergeParents))
		for i, p := range p.mergeParents {
			snapshots[i] = p.getSnapshotID()
		}
		return snapshots
	}
	return nil
}

// order is from parent->child, cr will be at end of slice
func (cr *cacheRecord) layerChain() (layers []*cacheRecord) {
	switch cr.parentKind() {
	case Merge:
		for _, parent := range cr.mergeParents {
			layers = append(layers, parent.layerChain()...)
		}
		return layers
	case Layer:
		layers = append(layers, cr.layerParent.layerChain()...)
		fallthrough
	case None:
		layers = append(layers, cr)
		return layers
	}
	return nil
}

// hold cacheRecord.mu lock before calling
func (cr *cacheRecord) layerDigestChain() []digest.Digest {
	if cr.layerDigestChainCache != nil {
		return cr.layerDigestChainCache
	}
	layerChain := cr.layerChain()
	dgsts := make([]digest.Digest, len(layerChain))
	for i, layer := range layerChain {
		dgst := layer.getBlob()
		if dgst == "" {
			return nil
		}
		dgsts[i] = dgst
	}
	cr.layerDigestChainCache = dgsts
	return dgsts
}

// hold ref lock before calling
func (cr *cacheRecord) isDead() bool {
	return cr.dead || (cr.equalImmutable != nil && cr.equalImmutable.dead) || (cr.equalMutable != nil && cr.equalMutable.dead)
}

var errSkipWalk = errors.New("skip")

// walkRecords calls the provided func on cr and each of its ancestors, counting both layer
// and merge parents. It starts at cr and does a depth-first walk to parents. It will visit
// a record and its parents multiple times if encountered more than once. It will only skip
// visiting parents of a record if errSkipWalk is returned. If any other error is returned,
// the walk will stop and return the error to the caller.
func (cr *cacheRecord) walkRecords(f func(*cacheRecord) error) error {
	curs := []*cacheRecord{cr}
	for len(curs) > 0 {
		cur := curs[len(curs)-1]
		curs = curs[:len(curs)-1]
		if err := f(cur); err != nil {
			if errors.Is(err, errSkipWalk) {
				continue
			}
			return err
		}
		switch cur.parentKind() {
		case Layer:
			curs = append(curs, cur.layerParent.cacheRecord)
		case Merge:
			for _, p := range cur.mergeParents {
				curs = append(curs, p.cacheRecord)
			}
		}
	}
	return nil
}

// walkUniqueRecords calls walkRecords but skips a record if it's already been visited.
func (cr *cacheRecord) walkUniqueRecords(f func(*cacheRecord) error) error {
	memo := make(map[*cacheRecord]struct{})
	return cr.walkRecords(func(cr *cacheRecord) error {
		if _, ok := memo[cr]; ok {
			return errSkipWalk
		}
		memo[cr] = struct{}{}
		return f(cr)
	})
}

func (cr *cacheRecord) isLazy(ctx context.Context) (bool, error) {
	if !cr.getBlobOnly() {
		return false, nil
	}
	dgst := cr.getBlob()
	// special case for moby where there is no compressed blob (empty digest)
	if dgst == "" {
		return false, nil
	}
	_, err := cr.cm.ContentStore.Info(ctx, dgst)
	if errors.Is(err, errdefs.ErrNotFound) {
		return true, nil
	} else if err != nil {
		return false, err
	}

	// If the snapshot is a remote snapshot, this layer is lazy.
	if info, err := cr.cm.Snapshotter.Stat(ctx, cr.getSnapshotID()); err == nil {
		if _, ok := info.Labels["containerd.io/snapshot/remote"]; ok {
			return true, nil
		}
	}

	return false, nil
}

func (cr *cacheRecord) IdentityMapping() *idtools.IdentityMapping {
	return cr.cm.IdentityMapping()
}

func (cr *cacheRecord) viewLeaseID() string {
	return cr.ID() + "-view"
}

func (cr *cacheRecord) viewSnapshotID() string {
	return cr.getSnapshotID() + "-view"
}

func (cr *cacheRecord) size(ctx context.Context) (int64, error) {
	// this expects that usage() is implemented lazily
	s, err := cr.sizeG.Do(ctx, cr.ID(), func(ctx context.Context) (interface{}, error) {
		cr.mu.Lock()
		s := cr.getSize()
		if s != sizeUnknown {
			cr.mu.Unlock()
			return s, nil
		}
		driverID := cr.getSnapshotID()
		if cr.equalMutable != nil {
			driverID = cr.equalMutable.getSnapshotID()
		}
		cr.mu.Unlock()
		var usage snapshots.Usage
		if !cr.getBlobOnly() {
			var err error
			usage, err = cr.cm.Snapshotter.Usage(ctx, driverID)
			if err != nil {
				cr.mu.Lock()
				isDead := cr.isDead()
				cr.mu.Unlock()
				if isDead {
					return int64(0), nil
				}
				if !errors.Is(err, errdefs.ErrNotFound) {
					return s, errors.Wrapf(err, "failed to get usage for %s", cr.ID())
				}
			}
		}
		if dgst := cr.getBlob(); dgst != "" {
			info, err := cr.cm.ContentStore.Info(ctx, digest.Digest(dgst))
			if err == nil {
				usage.Size += info.Size
			}
			for k, v := range info.Labels {
				// accumulate size of compression variant blobs
				if strings.HasPrefix(k, compressionVariantDigestLabelPrefix) {
					if cdgst, err := digest.Parse(v); err == nil {
						if digest.Digest(dgst) == cdgst {
							// do not double count if the label points to this content itself.
							continue
						}
						if info, err := cr.cm.ContentStore.Info(ctx, cdgst); err == nil {
							usage.Size += info.Size
						}
					}
				}
			}
		}
		cr.mu.Lock()
		cr.queueSize(usage.Size)
		if err := cr.commitMetadata(); err != nil {
			cr.mu.Unlock()
			return s, err
		}
		cr.mu.Unlock()
		return usage.Size, nil
	})
	if err != nil {
		return 0, err
	}
	return s.(int64), nil
}

// call when holding the manager lock
func (cr *cacheRecord) remove(ctx context.Context, removeSnapshot bool) error {
	delete(cr.cm.records, cr.ID())
	if removeSnapshot {
		if err := cr.cm.LeaseManager.Delete(ctx, leases.Lease{
			ID: cr.ID(),
		}); err != nil {
			return errors.Wrapf(err, "failed to delete lease for %s", cr.ID())
		}
	}
	if err := cr.cm.MetadataStore.Clear(cr.ID()); err != nil {
		return errors.Wrapf(err, "failed to delete metadata of %s", cr.ID())
	}
	if err := cr.parentRefs.release(ctx); err != nil {
		return errors.Wrapf(err, "failed to release parents of %s", cr.ID())
	}
	return nil
}

type immutableRef struct {
	*cacheRecord
	triggerLastUsed bool
	descHandlers    DescHandlers
}

func (sr *immutableRef) DescHandler(dgst digest.Digest) *DescHandler {
	return sr.descHandlers[dgst]
}

type mutableRef struct {
	*cacheRecord
	triggerLastUsed bool
	descHandlers    DescHandlers
}

func (sr *mutableRef) DescHandler(dgst digest.Digest) *DescHandler {
	return sr.descHandlers[dgst]
}

func (sr *immutableRef) Clone() ImmutableRef {
	sr.mu.Lock()
	ref := sr.ref(false, sr.descHandlers)
	sr.mu.Unlock()
	return ref
}

func (sr *immutableRef) clone() *immutableRef {
	sr.mu.Lock()
	ref := sr.ref(false, sr.descHandlers)
	sr.mu.Unlock()
	return ref
}

func (cr *cacheRecord) ociDesc(ctx context.Context, dhs DescHandlers) (ocispecs.Descriptor, error) {
	dgst := cr.getBlob()
	if dgst == "" {
		return ocispecs.Descriptor{}, errors.Errorf("no blob set for cache record %s", cr.ID())
	}

	desc := ocispecs.Descriptor{
		Digest:      cr.getBlob(),
		Size:        cr.getBlobSize(),
		MediaType:   cr.getMediaType(),
		Annotations: make(map[string]string),
	}

	if blobDesc, err := getBlobDesc(ctx, cr.cm.ContentStore, desc.Digest); err == nil {
		if blobDesc.Annotations != nil {
			desc.Annotations = blobDesc.Annotations
		}
	} else if dh, ok := dhs[desc.Digest]; ok {
		// No blob metadtata is stored in the content store. Try to get annotations from desc handlers.
		for k, v := range filterAnnotationsForSave(dh.Annotations) {
			desc.Annotations[k] = v
		}
	}

	diffID := cr.getDiffID()
	if diffID != "" {
		desc.Annotations["containerd.io/uncompressed"] = string(diffID)
	}

	createdAt := cr.GetCreatedAt()
	if !createdAt.IsZero() {
		createdAt, err := createdAt.MarshalText()
		if err != nil {
			return ocispecs.Descriptor{}, err
		}
		desc.Annotations["buildkit/createdat"] = string(createdAt)
	}

	if description := cr.GetDescription(); description != "" {
		desc.Annotations["buildkit/description"] = description
	}

	return desc, nil
}

const (
	compressionVariantDigestLabelPrefix      = "buildkit.io/compression/digest."
	compressionVariantAnnotationsLabelPrefix = "buildkit.io/compression/annotation."
	compressionVariantMediaTypeLabel         = "buildkit.io/compression/mediatype"
)

func compressionVariantDigestLabel(compressionType compression.Type) string {
	return compressionVariantDigestLabelPrefix + compressionType.String()
}

func (cr *cacheRecord) getCompressionBlob(ctx context.Context, compressionType compression.Type) (ocispecs.Descriptor, error) {
	cs := cr.cm.ContentStore
	info, err := cs.Info(ctx, cr.getBlob())
	if err != nil {
		return ocispecs.Descriptor{}, err
	}
	dgstS, ok := info.Labels[compressionVariantDigestLabel(compressionType)]
	if ok {
		dgst, err := digest.Parse(dgstS)
		if err != nil {
			return ocispecs.Descriptor{}, err
		}
		return getBlobDesc(ctx, cs, dgst)
	}
	return ocispecs.Descriptor{}, errdefs.ErrNotFound
}

func (cr *cacheRecord) addCompressionBlob(ctx context.Context, desc ocispecs.Descriptor, compressionType compression.Type) error {
	cs := cr.cm.ContentStore
	if err := cr.cm.LeaseManager.AddResource(ctx, leases.Lease{ID: cr.ID()}, leases.Resource{
		ID:   desc.Digest.String(),
		Type: "content",
	}); err != nil {
		return err
	}
	info, err := cs.Info(ctx, cr.getBlob())
	if err != nil {
		return err
	}
	if info.Labels == nil {
		info.Labels = make(map[string]string)
	}
	cachedVariantLabel := compressionVariantDigestLabel(compressionType)
	info.Labels[cachedVariantLabel] = desc.Digest.String()
	if _, err := cs.Update(ctx, info, "labels."+cachedVariantLabel); err != nil {
		return err
	}

	info, err = cs.Info(ctx, desc.Digest)
	if err != nil {
		return err
	}
	var fields []string
	info.Labels = map[string]string{
		compressionVariantMediaTypeLabel: desc.MediaType,
	}
	fields = append(fields, "labels."+compressionVariantMediaTypeLabel)
	for k, v := range filterAnnotationsForSave(desc.Annotations) {
		k2 := compressionVariantAnnotationsLabelPrefix + k
		info.Labels[k2] = v
		fields = append(fields, "labels."+k2)
	}
	if _, err := cs.Update(ctx, info, fields...); err != nil {
		return err
	}

	return nil
}

func filterAnnotationsForSave(a map[string]string) (b map[string]string) {
	if a == nil {
		return nil
	}
	for _, k := range append(eStargzAnnotations, containerdUncompressed) {
		v, ok := a[k]
		if !ok {
			continue
		}
		if b == nil {
			b = make(map[string]string)
		}
		b[k] = v
	}
	return
}

func getBlobDesc(ctx context.Context, cs content.Store, dgst digest.Digest) (ocispecs.Descriptor, error) {
	info, err := cs.Info(ctx, dgst)
	if err != nil {
		return ocispecs.Descriptor{}, err
	}
	if info.Labels == nil {
		return ocispecs.Descriptor{}, fmt.Errorf("no blob metadata is stored for %q", info.Digest)
	}
	mt, ok := info.Labels[compressionVariantMediaTypeLabel]
	if !ok {
		return ocispecs.Descriptor{}, fmt.Errorf("no media type is stored for %q", info.Digest)
	}
	desc := ocispecs.Descriptor{
		Digest:    info.Digest,
		Size:      info.Size,
		MediaType: mt,
	}
	for k, v := range info.Labels {
		if strings.HasPrefix(k, compressionVariantAnnotationsLabelPrefix) {
			if desc.Annotations == nil {
				desc.Annotations = make(map[string]string)
			}
			desc.Annotations[strings.TrimPrefix(k, compressionVariantAnnotationsLabelPrefix)] = v
		}
	}
	return desc, nil
}

func (sr *immutableRef) Mount(ctx context.Context, readonly bool, s session.Group) (snapshot.Mountable, error) {
	if !readonly {
		return nil, errors.New("immutable refs must be mounted read-only")
	}
	return sr.cacheRecord.Mount(ctx, true, sr.descHandlers, s)
}

func (sr *immutableRef) Extract(ctx context.Context, s session.Group) (rerr error) {
	if !sr.getBlobOnly() {
		return
	}
	if sr.cm.Applier == nil {
		return errors.New("extract requires an applier")
	}
	return sr.cacheRecord.PrepareMount(ctx, sr.descHandlers, s)
}

func (cr *cacheRecord) Mount(ctx context.Context, readonly bool, dhs DescHandlers, s session.Group) (snapshot.Mountable, error) {
	if err := cr.PrepareMount(ctx, dhs, s); err != nil {
		return nil, err
	}
	mnt := cr.mountCache
	if readonly {
		mnt = setReadonly(mnt)
	}

	return mnt, nil
}

func (cr *cacheRecord) PrepareMount(ctx context.Context, dhs DescHandlers, s session.Group) (rerr error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	if cr.mountCache != nil {
		return nil
	}

	ctx, done, err := leaseutil.WithLease(ctx, cr.cm.LeaseManager, leaseutil.MakeTemporary)
	if err != nil {
		return err
	}
	defer done(ctx)

	if cr.GetLayerType() == "windows" {
		ctx = winlayers.UseWindowsLayerMode(ctx)
	}

	if cr.cm.Snapshotter.Name() == "stargz" {
		if err := cr.withRemoteSnapshotLabelsStargzMode(ctx, dhs, s, func() {
			if rerr = cr.prepareRemoteSnapshotsStargzMode(ctx, dhs, s); rerr != nil {
				return
			}
			rerr = cr.prepareMount(ctx, dhs, s)
		}); err != nil {
			return err
		}
		return rerr
	}

	return cr.prepareMount(ctx, dhs, s)
}

func (cr *cacheRecord) withRemoteSnapshotLabelsStargzMode(ctx context.Context, dhs DescHandlers, s session.Group, f func()) error {
	for _, r := range cr.layerChain() {
		r := r
		info, err := r.cm.Snapshotter.Stat(ctx, r.getSnapshotID())
		if err != nil && !errdefs.IsNotFound(err) {
			return err
		} else if errdefs.IsNotFound(err) {
			continue // This snpashot doesn't exist; skip
		} else if _, ok := info.Labels["containerd.io/snapshot/remote"]; !ok {
			continue // This isn't a remote snapshot; skip
		}
		dh := dhs[digest.Digest(r.getBlob())]
		if dh == nil {
			continue // no info passed; skip
		}

		// Append temporary labels (based on dh.SnapshotLabels) as hints for remote snapshots.
		// For avoiding collosion among calls, keys of these tmp labels contain an unique ID.
		flds, labels := makeTmpLabelsStargzMode(snapshots.FilterInheritedLabels(dh.SnapshotLabels), s)
		info.Labels = labels
		if _, err := r.cm.Snapshotter.Update(ctx, info, flds...); err != nil {
			return errors.Wrapf(err, "failed to add tmp remote labels for remote snapshot")
		}
		defer func() {
			for k := range info.Labels {
				info.Labels[k] = "" // Remove labels appended in this call
			}
			if _, err := r.cm.Snapshotter.Update(ctx, info, flds...); err != nil {
				logrus.Warn(errors.Wrapf(err, "failed to remove tmp remote labels"))
			}
		}()

		continue
	}

	f()

	return nil
}

func (cr *cacheRecord) prepareRemoteSnapshotsStargzMode(ctx context.Context, dhs DescHandlers, s session.Group) error {
	_, err := cr.sizeG.Do(ctx, cr.ID()+"-prepare-remote-snapshot", func(ctx context.Context) (_ interface{}, rerr error) {
		for _, r := range cr.layerChain() {
			r := r
			snapshotID := r.getSnapshotID()
			if _, err := r.cm.Snapshotter.Stat(ctx, snapshotID); err == nil {
				continue
			}

			dh := dhs[digest.Digest(r.getBlob())]
			if dh == nil {
				// We cannot prepare remote snapshots without descHandler.
				return nil, nil
			}

			// tmpLabels contains dh.SnapshotLabels + session IDs. All keys contain
			// an unique ID for avoiding the collision among snapshotter API calls to
			// this snapshot. tmpLabels will be removed at the end of this function.
			defaultLabels := snapshots.FilterInheritedLabels(dh.SnapshotLabels)
			if defaultLabels == nil {
				defaultLabels = make(map[string]string)
			}
			tmpFields, tmpLabels := makeTmpLabelsStargzMode(defaultLabels, s)
			defaultLabels["containerd.io/snapshot.ref"] = snapshotID

			// Prepare remote snapshots
			var (
				key  = fmt.Sprintf("tmp-%s %s", identity.NewID(), r.getChainID())
				opts = []snapshots.Opt{
					snapshots.WithLabels(defaultLabels),
					snapshots.WithLabels(tmpLabels),
				}
			)
			parentID := ""
			if r.layerParent != nil {
				parentID = r.layerParent.getSnapshotID()
			}
			if err := r.cm.Snapshotter.Prepare(ctx, key, parentID, opts...); err != nil {
				if errdefs.IsAlreadyExists(err) {
					// Check if the targeting snapshot ID has been prepared as
					// a remote snapshot in the snapshotter.
					info, err := r.cm.Snapshotter.Stat(ctx, snapshotID)
					if err == nil { // usable as remote snapshot without unlazying.
						defer func() {
							// Remove tmp labels appended in this func
							for k := range tmpLabels {
								info.Labels[k] = ""
							}
							if _, err := r.cm.Snapshotter.Update(ctx, info, tmpFields...); err != nil {
								logrus.Warn(errors.Wrapf(err,
									"failed to remove tmp remote labels after prepare"))
							}
						}()

						// Try the next layer as well.
						continue
					}
				}
			}

			// This layer and all upper layers cannot be prepared without unlazying.
			break
		}

		return nil, nil
	})
	return err
}

func makeTmpLabelsStargzMode(labels map[string]string, s session.Group) (fields []string, res map[string]string) {
	res = make(map[string]string)
	// Append unique ID to labels for avoiding collision of labels among calls
	id := identity.NewID()
	for k, v := range labels {
		tmpKey := k + "." + id
		fields = append(fields, "labels."+tmpKey)
		res[tmpKey] = v
	}
	for i, sid := range session.AllSessionIDs(s) {
		sidKey := "containerd.io/snapshot/remote/stargz.session." + fmt.Sprintf("%d", i) + "." + id
		fields = append(fields, "labels."+sidKey)
		res[sidKey] = sid
	}
	return
}

func (cr *cacheRecord) prepareMount(ctx context.Context, dhs DescHandlers, s session.Group) error {
	_, err := cr.sizeG.Do(ctx, cr.ID()+"-prepare-mount", func(ctx context.Context) (_ interface{}, rerr error) {
		if cr.mountCache != nil {
			return nil, nil
		}
		snapshotID := cr.getSnapshotID()
		if _, err := cr.cm.Snapshotter.Stat(ctx, snapshotID); err != nil {
			if !errdefs.IsNotFound(err) {
				return nil, err
			}
			eg, egctx := errgroup.WithContext(ctx)

			switch cr.parentKind() {
			case Layer:
				eg.Go(func() error {
					if err := cr.layerParent.prepareMount(egctx, dhs, s); err != nil {
						return err
					}
					return nil
				})
			case Merge:
				for _, parent := range cr.mergeParents {
					parent := parent
					eg.Go(func() error {
						if err := parent.prepareMount(egctx, dhs, s); err != nil {
							return err
						}
						return nil
					})
				}
			}

			var dh *DescHandler
			var desc ocispecs.Descriptor
			if cr.getBlobOnly() {
				var err error
				desc, err = cr.ociDesc(ctx, dhs)
				if err != nil {
					return nil, err
				}
				dh = dhs[desc.Digest]

				eg.Go(func() error {
					// unlazies if needed, otherwise a no-op
					return lazyRefProvider{
						rec:     cr,
						desc:    desc,
						dh:      dh,
						session: s,
					}.Unlazy(egctx)
				})
			}

			if err := eg.Wait(); err != nil {
				return nil, err
			}

			if cr.getBlobOnly() {
				if dh != nil && dh.Progress != nil {
					_, stopProgress := dh.Progress.Start(ctx)
					defer stopProgress(rerr)
					statusDone := dh.Progress.Status("extracting "+desc.Digest.String(), "extracting")
					defer statusDone()
				}

				var parentSnapshot string
				if cr.layerParent != nil {
					parentSnapshot = cr.layerParent.getSnapshotID()
				}
				tmpSnapshotID := identity.NewID()
				if err := cr.cm.Snapshotter.Prepare(ctx, tmpSnapshotID, parentSnapshot); err != nil {
					if !errors.Is(err, errdefs.ErrAlreadyExists) {
						return nil, errors.Wrap(err, "failed to prepare for extraction")
					}
				}

				mountable, err := cr.cm.Snapshotter.Mounts(ctx, tmpSnapshotID)
				if err != nil {
					return nil, errors.Wrap(err, "failed to get mountable for extraction")
				}
				mounts, unmount, err := mountable.Mount()
				if err != nil {
					return nil, errors.Wrap(err, "failed to get mounts for extraction")
				}
				_, err = cr.cm.Applier.Apply(ctx, desc, mounts)
				if err != nil {
					unmount()
					return nil, errors.Wrap(err, "failed to apply blob extraction")
				}

				if err := unmount(); err != nil {
					return nil, errors.Wrap(err, "failed to unmount extraction target")
				}
				if err := cr.cm.Snapshotter.Commit(ctx, snapshotID, tmpSnapshotID); err != nil {
					if !errors.Is(err, errdefs.ErrAlreadyExists) {
						return nil, errors.Wrap(err, "failed to commit extraction")
					}
				}
				cr.queueBlobOnly(false)
				cr.queueSize(sizeUnknown)
				if err := cr.commitMetadata(); err != nil {
					return nil, err
				}
			}
		}

		var mountSnapshotID string
		switch cr.parentKind() {
		case Layer, None:
			if cr.mutable {
				mountSnapshotID = snapshotID
			} else if cr.equalMutable != nil {
				mountSnapshotID = cr.equalMutable.getSnapshotID()
			} else {
				mountSnapshotID = cr.viewSnapshotID()
			}
		case Merge:
			mountSnapshotID = cr.viewSnapshotID()
			if err := cr.cm.Snapshotter.Merge(ctx, snapshotID, cr.parentSnapshotIDs()); err != nil && !errdefs.IsAlreadyExists(err) {
				return nil, err
			}
		}

		if mountSnapshotID == cr.viewSnapshotID() {
			if _, err := cr.cm.LeaseManager.Create(ctx, func(l *leases.Lease) error {
				l.ID = cr.viewLeaseID()
				l.Labels = map[string]string{
					"containerd.io/gc.flat": time.Now().UTC().Format(time.RFC3339Nano),
				}
				return nil
			}, leaseutil.MakeTemporary); err != nil && !errdefs.IsAlreadyExists(err) {
				return nil, err
			}
			defer func() {
				if rerr != nil {
					cr.cm.LeaseManager.Delete(context.TODO(), leases.Lease{ID: cr.viewLeaseID()})
				}
			}()
			if err := cr.cm.LeaseManager.AddResource(ctx, leases.Lease{ID: cr.viewLeaseID()}, leases.Resource{
				ID:   mountSnapshotID,
				Type: "snapshots/" + cr.cm.Snapshotter.Name(),
			}); err != nil && !errdefs.IsAlreadyExists(err) {
				return nil, err
			}
			if _, err := cr.cm.Snapshotter.View(ctx, mountSnapshotID, snapshotID); err != nil && !errdefs.IsAlreadyExists(err) {
				return nil, err
			}
		}

		mountable, err := cr.cm.Snapshotter.Mounts(ctx, mountSnapshotID)
		if err != nil {
			return nil, err
		}
		cr.mountCache = mountable
		return nil, nil
	})
	return err
}

func (sr *immutableRef) Release(ctx context.Context) error {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.release(ctx)
}

func (sr *immutableRef) shouldUpdateLastUsed() bool {
	return sr.triggerLastUsed
}

func (sr *immutableRef) updateLastUsedNow() bool {
	if !sr.triggerLastUsed {
		return false
	}
	for r := range sr.refs {
		if r.shouldUpdateLastUsed() {
			return false
		}
	}
	return true
}

func (sr *immutableRef) release(ctx context.Context) error {
	delete(sr.refs, sr)

	if sr.updateLastUsedNow() {
		sr.updateLastUsed()
		if sr.equalMutable != nil {
			sr.equalMutable.triggerLastUsed = true
		}
	}

	if len(sr.refs) == 0 {
		if sr.equalMutable != nil {
			sr.equalMutable.release(ctx)
		} else {
			if err := sr.cm.LeaseManager.Delete(ctx, leases.Lease{ID: sr.viewLeaseID()}); err != nil && !errdefs.IsNotFound(err) {
				return err
			}
			sr.mountCache = nil
		}
	}

	return nil
}

func (sr *immutableRef) finalizeLocked(ctx context.Context) error {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.finalize(ctx)
}

// caller must hold cacheRecord.mu
func (cr *cacheRecord) finalize(ctx context.Context) error {
	mutable := cr.equalMutable
	if mutable == nil {
		return nil
	}

	_, err := cr.cm.LeaseManager.Create(ctx, func(l *leases.Lease) error {
		l.ID = cr.ID()
		l.Labels = map[string]string{
			"containerd.io/gc.flat": time.Now().UTC().Format(time.RFC3339Nano),
		}
		return nil
	})
	if err != nil {
		if !errors.Is(err, errdefs.ErrAlreadyExists) { // migrator adds leases for everything
			return errors.Wrap(err, "failed to create lease")
		}
	}

	if err := cr.cm.LeaseManager.AddResource(ctx, leases.Lease{ID: cr.ID()}, leases.Resource{
		ID:   cr.getSnapshotID(),
		Type: "snapshots/" + cr.cm.Snapshotter.Name(),
	}); err != nil {
		cr.cm.LeaseManager.Delete(context.TODO(), leases.Lease{ID: cr.ID()})
		return errors.Wrapf(err, "failed to add snapshot %s to lease", cr.getSnapshotID())
	}

	if err := cr.cm.Snapshotter.Commit(ctx, cr.getSnapshotID(), mutable.getSnapshotID()); err != nil {
		cr.cm.LeaseManager.Delete(context.TODO(), leases.Lease{ID: cr.ID()})
		return errors.Wrapf(err, "failed to commit %s to %s", mutable.getSnapshotID(), cr.getSnapshotID())
	}
	cr.mountCache = nil

	mutable.dead = true
	go func() {
		cr.cm.mu.Lock()
		defer cr.cm.mu.Unlock()
		if err := mutable.remove(context.TODO(), true); err != nil {
			logrus.Error(err)
		}
	}()

	cr.equalMutable = nil
	cr.clearEqualMutable()
	return cr.commitMetadata()
}

func (sr *mutableRef) shouldUpdateLastUsed() bool {
	return sr.triggerLastUsed
}

func (sr *mutableRef) commit(ctx context.Context) (_ *immutableRef, rerr error) {
	if !sr.mutable || len(sr.refs) == 0 {
		return nil, errors.Wrapf(errInvalid, "invalid mutable ref %p", sr)
	}

	id := identity.NewID()
	md, _ := sr.cm.getMetadata(id)
	rec := &cacheRecord{
		mu:            sr.mu,
		cm:            sr.cm,
		parentRefs:    sr.parentRefs.clone(),
		equalMutable:  sr,
		refs:          make(map[ref]struct{}),
		cacheMetadata: md,
	}

	if descr := sr.GetDescription(); descr != "" {
		if err := md.queueDescription(descr); err != nil {
			return nil, err
		}
	}

	if err := initializeMetadata(rec.cacheMetadata, rec.parentRefs); err != nil {
		return nil, err
	}

	sr.cm.records[id] = rec

	if err := sr.commitMetadata(); err != nil {
		return nil, err
	}

	md.queueCommitted(true)
	md.queueSize(sizeUnknown)
	md.queueSnapshotID(id)
	md.setEqualMutable(sr.ID())
	if err := md.commitMetadata(); err != nil {
		return nil, err
	}

	ref := rec.ref(true, sr.descHandlers)
	sr.equalImmutable = ref
	return ref, nil
}

func (sr *mutableRef) Mount(ctx context.Context, readonly bool, s session.Group) (snapshot.Mountable, error) {
	return sr.cacheRecord.Mount(ctx, readonly, sr.descHandlers, s)
}

func (sr *mutableRef) Commit(ctx context.Context) (ImmutableRef, error) {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.commit(ctx)
}

func (sr *mutableRef) Release(ctx context.Context) error {
	sr.cm.mu.Lock()
	defer sr.cm.mu.Unlock()

	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.release(ctx)
}

func (sr *mutableRef) release(ctx context.Context) error {
	delete(sr.refs, sr)
	if !sr.HasCachePolicyRetain() {
		if sr.equalImmutable != nil {
			if sr.equalImmutable.HasCachePolicyRetain() {
				if sr.shouldUpdateLastUsed() {
					sr.updateLastUsed()
					sr.triggerLastUsed = false
				}
				return nil
			}
			if err := sr.equalImmutable.remove(ctx, false); err != nil {
				return err
			}
		}
		return sr.remove(ctx, true)
	}
	if sr.shouldUpdateLastUsed() {
		sr.updateLastUsed()
		sr.triggerLastUsed = false
	}
	return nil
}

func setReadonly(mounts snapshot.Mountable) snapshot.Mountable {
	return &readOnlyMounter{mounts}
}

type readOnlyMounter struct {
	snapshot.Mountable
}

func (m *readOnlyMounter) Mount() ([]mount.Mount, func() error, error) {
	mounts, release, err := m.Mountable.Mount()
	if err != nil {
		return nil, nil, err
	}
	for i, m := range mounts {
		if m.Type == "overlay" {
			mounts[i].Options = readonlyOverlay(m.Options)
			continue
		}
		opts := make([]string, 0, len(m.Options))
		for _, opt := range m.Options {
			if opt != "rw" {
				opts = append(opts, opt)
			}
		}
		opts = append(opts, "ro")
		mounts[i].Options = opts
	}
	return mounts, release, nil
}

func readonlyOverlay(opt []string) []string {
	out := make([]string, 0, len(opt))
	upper := ""
	for _, o := range opt {
		if strings.HasPrefix(o, "upperdir=") {
			upper = strings.TrimPrefix(o, "upperdir=")
		} else if !strings.HasPrefix(o, "workdir=") {
			out = append(out, o)
		}
	}
	if upper != "" {
		for i, o := range out {
			if strings.HasPrefix(o, "lowerdir=") {
				out[i] = "lowerdir=" + upper + ":" + strings.TrimPrefix(o, "lowerdir=")
			}
		}
	}
	return out
}
