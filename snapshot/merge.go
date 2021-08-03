package snapshot

import (
	"context"
	"strconv"
	"strings"

	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/pkg/userns"
	"github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/pkg/errors"
)

// hardlinkMergeSnapshotters are the names of snapshotters that support merges implemented by
// creating "hardlink farms" where non-directory objects are hard-linked into the merged tree
// from their parent snapshots.
var hardlinkMergeSnapshotters = map[string]struct{}{
	"native":    {},
	"overlayfs": {},
	"stargz":    {},
}

// overlayBasedSnapshotters are the names of snapshotter that use overlay mounts, which
// enables optimizations such as skipping the base layer when doing a hardlink merge.
var overlayBasedSnapshotters = map[string]struct{}{
	"overlayfs": {},
	"stargz":    {},
}

type MergeSnapshotter interface {
	Snapshotter
	// Merge creates a snapshot whose contents are the merged contents of each of the provided
	// parent keys. This merged snapshot is equivalent to the result of taking each layer of the
	// provided parent snapshots and applying them on top of each other, in the order provided,
	// lowest->highest.
	//
	// Each parent key is expected to be either a committed snapshot. The snapshot created by
	// Merge is also committed.
	//
	// The size of a merged snapshot (as returned by the Usage method) depends on the merge
	// implementation. Implementations using hardlinks to create merged views will take up
	// less space than those that use copies, for example.
	Merge(ctx context.Context, key string, parents []string, opts ...snapshots.Opt) error
}

type mergeSnapshotter struct {
	Snapshotter
	lm leases.Manager

	// Whether we should try to implement merges by hardlinking between underlying directories
	useHardlinks bool

	// Whether we can re-use the first merge input or have to create all merges from scratch.
	// On overlay-based snapshotters, we can use the base merge input as a parent w/out
	// needing to copy it.
	reuseBaseMergeInput bool
}

func NewMergeSnapshotter(ctx context.Context, sn Snapshotter, lm leases.Manager) MergeSnapshotter {
	name := sn.Name()
	_, useHardlinks := hardlinkMergeSnapshotters[name]
	_, overlayBased := overlayBasedSnapshotters[name]

	if overlayBased && userns.RunningInUserNS() {
		// When using an overlay-based snapshotter, if we are running rootless on a pre-5.11
		// kernel, we will not have userxattr. This results in opaque xattrs not being visible
		// to us and thus breaking the overlay-optimized differ. This also means that there are
		// cases where in order to apply a deletion, we'd need to create a whiteout device but
		// may not have access to one to hardlink, so we just fall back to not using hardlinks
		// at all in this case.
		userxattr, err := needsUserXAttr(ctx, sn, lm)
		if err != nil {
			bklog.G(ctx).Debugf("failed to check user xattr: %v", err)
			useHardlinks = false
		} else {
			useHardlinks = userxattr
		}
	}

	return &mergeSnapshotter{
		Snapshotter:         sn,
		lm:                  lm,
		useHardlinks:        useHardlinks,
		reuseBaseMergeInput: overlayBased,
	}
}

func (sn *mergeSnapshotter) Merge(ctx context.Context, key string, parents []string, opts ...snapshots.Opt) error {
	// Flatten the provided parent keys out. If any of them are them selves merges, don't use that key directly as
	// a parent and instead use each of the merge inputs to that parent directly in the merge here. This is important
	// for ensuring deletions defined between layers are preserved as part of each merge.
	var mergeKeys []string
	for _, parentKey := range parents {
		parentInfo, err := sn.Stat(ctx, parentKey)
		if err != nil {
			return errors.Wrap(err, "failed to stat parent during merge")
		}
		if parentInfo.Kind != snapshots.KindCommitted {
			return errors.Wrapf(err, "invalid kind %q for parent key %q provided to merge", parentInfo.Kind, parentKey)
		}
		if parentKeys := mergeKeysOf(parentInfo); parentKeys != nil {
			mergeKeys = append(mergeKeys, parentKeys...)
		} else {
			mergeKeys = append(mergeKeys, parentKey)
		}
	}

	// Map each key in mergeKeys to each key making up the snapshot chain.
	// applyChains' outer slice has the same order as parents, which is expected to be lowest merge input to highest.
	// applyChains' inner slice is ordered from highest layer to lowest.
	var applyChains [][]string
	var baseKey string
	for i, parent := range mergeKeys {
		if i == 0 && sn.reuseBaseMergeInput {
			// Overlay-based snapshotters can skip the base of the merge and just use it as the parent of the merge snapshot.
			// Other snapshotters will start empty (with baseKey set to "")
			baseKey = parent
			continue
		}
		var chain []string
		for curkey := parent; curkey != ""; {
			info, err := sn.Stat(ctx, curkey)
			if err != nil {
				return errors.Wrapf(err, "failed to stat chain key %q", curkey)
			}
			chain = append(chain, info.Name)
			curkey = info.Parent
		}
		// add an empty key as the bottom layer so that the real bottom layer gets diffed with it and applied
		chain = append(chain, "")
		applyChains = append(applyChains, chain)
	}

	// Make the snapshot that will be merged into
	prepareKey := identity.NewID()
	if err := sn.Prepare(ctx, prepareKey, baseKey); err != nil {
		return errors.Wrapf(err, "failed to prepare %q", key)
	}
	applyMounts, err := sn.Mounts(ctx, prepareKey)
	if err != nil {
		return errors.Wrapf(err, "failed to get mounts of %q", key)
	}

	tempLeaseCtx, done, err := leaseutil.WithLease(ctx, sn.lm, leaseutil.MakeTemporary)
	if err != nil {
		return errors.Wrap(err, "failed to create temporary lease for view mounts during merge")
	}
	defer done(context.TODO())

	// externalHardlinks keeps track of which inodes have been hard-linked between snapshots (which is
	//	enabled when sn.useHardlinks is set to true)
	externalHardlinks := make(map[uint64]struct{})

	// Iterate over (lower, upper) pairs in each applyChain, calculating their diffs and applying each
	// one to the mount.
	// TODO: note for @tonistiigi, this is where we are currently flattening input snapshots for both
	// native and overlay implementations. What is the advantage of splitting this into separate snapshots
	// for the overlay case?
	for _, chain := range applyChains {
		for j := range chain[:len(chain)-1] {
			lower := chain[len(chain)-1-j]
			upper := chain[len(chain)-2-j]

			var lowerMounts Mountable
			if lower != "" {
				viewID := identity.NewID()
				var err error
				lowerMounts, err = sn.View(tempLeaseCtx, viewID, lower)
				if err != nil {
					return errors.Wrapf(err, "failed to get mounts of lower %q", lower)
				}
			}

			viewID := identity.NewID()
			upperMounts, err := sn.View(tempLeaseCtx, viewID, upper)
			if err != nil {
				return errors.Wrapf(err, "failed to get mounts of upper %q", upper)
			}

			err = diffApply(tempLeaseCtx, lowerMounts, upperMounts, applyMounts, sn.useHardlinks, externalHardlinks)
			if err != nil {
				return err
			}
		}
	}

	// save the correctly calculated usage as a label on the committed key
	usage, err := diskUsage(ctx, applyMounts, externalHardlinks)
	if err != nil {
		return errors.Wrap(err, "failed to get disk usage of diff apply merge")
	}
	if err := sn.Commit(ctx, key, prepareKey, withMergeUsage(usage), withMergeKeys(mergeKeys)); err != nil {
		return errors.Wrapf(err, "failed to commit %q", key)
	}
	return nil
}

func (sn *mergeSnapshotter) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	// If key was created by Merge, we may need to use the annotated mergeUsage key as
	// the snapshotter's usage method is wrong when hardlinks are used to create the merge.
	if info, err := sn.Stat(ctx, key); err != nil {
		return snapshots.Usage{}, err
	} else if usage, ok, err := mergeUsageOf(info); err != nil {
		return snapshots.Usage{}, err
	} else if ok {
		return usage, nil
	}
	return sn.Snapshotter.Usage(ctx, key)
}

// mergeUsage{Size,Inodes}Label hold the correct usage calculations for diffApplyMerges, for which the builtin usage
// is wrong because it can't account for hardlinks made across immutable snapshots
const mergeUsageSizeLabel = "buildkit.mergeUsageSize"
const mergeUsageInodesLabel = "buildkit.mergeUsageInodes"

func withMergeUsage(usage snapshots.Usage) snapshots.Opt {
	return snapshots.WithLabels(map[string]string{
		mergeUsageSizeLabel:   strconv.Itoa(int(usage.Size)),
		mergeUsageInodesLabel: strconv.Itoa(int(usage.Inodes)),
	})
}

func mergeUsageOf(info snapshots.Info) (usage snapshots.Usage, ok bool, rerr error) {
	if info.Labels == nil {
		return snapshots.Usage{}, false, nil
	}
	if str, ok := info.Labels[mergeUsageSizeLabel]; ok {
		i, err := strconv.Atoi(str)
		if err != nil {
			return snapshots.Usage{}, false, err
		}
		usage.Size = int64(i)
	}
	if str, ok := info.Labels[mergeUsageInodesLabel]; ok {
		i, err := strconv.Atoi(str)
		if err != nil {
			return snapshots.Usage{}, false, err
		}
		usage.Inodes = int64(i)
	}
	return usage, true, nil
}

// mergeKeysLabel holds the keys that a given snapshot should be merged on top of in order to be mounted
const mergeKeysLabel = "containerd.io/snapshot/buildkit.mergeKeys"

func withMergeKeys(keys []string) snapshots.Opt {
	return func(info *snapshots.Info) error {
		// make sure no keys have "," in them
		for _, k := range keys {
			if strings.Contains(k, ",") {
				return errors.Errorf("invalid merge key containing \",\": %s", k)
			}
		}
		return snapshots.WithLabels(map[string]string{
			mergeKeysLabel: strings.Join(keys, ","),
		})(info)
	}
}

func mergeKeysOf(info snapshots.Info) []string {
	if v := info.Labels[mergeKeysLabel]; v != "" {
		return strings.Split(v, ",")
	}
	return nil
}
