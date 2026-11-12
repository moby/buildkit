package cacheimport

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	cacheimporttypes "github.com/moby/buildkit/cache/remotecache/v1/types"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// sortConfig sorts the config structure to make sure it is deterministic.
//
// The resulting config only depends on the content of cc, never on the order
// of cc.Layers and cc.Records (which follows map iteration order when the
// config is built from the cache chains):
//   - layers are ordered by blob digest, then by the blob digests of their
//     whole parent chain;
//   - records are ordered by digest, then by a canonical key computed
//     recursively from their results and from the canonical keys of their
//     inputs. Records with identical canonical keys are interchangeable, so
//     they are merged into one, otherwise the links pointing to them would
//     still depend on the input order.
func sortConfig(cc *cacheimporttypes.CacheConfig) {
	layerKeys := canonicalLayerKeys(cc.Layers)

	layerOrder := make([]int, len(cc.Layers))
	for i := range layerOrder {
		layerOrder[i] = i
	}
	slices.SortStableFunc(layerOrder, func(a, b int) int {
		return cmp.Or(
			cmp.Compare(cc.Layers[a].Blob, cc.Layers[b].Blob),
			cmp.Compare(layerKeys[a], layerKeys[b]),
		)
	})
	newLayerIndex := make([]int, len(cc.Layers))
	for newIdx, oldIdx := range layerOrder {
		newLayerIndex[oldIdx] = newIdx
	}
	layers := make([]cacheimporttypes.CacheLayer, len(layerOrder))
	for newIdx, oldIdx := range layerOrder {
		l := cc.Layers[oldIdx]
		if l.ParentIndex >= 0 && l.ParentIndex < len(cc.Layers) {
			l.ParentIndex = newLayerIndex[l.ParentIndex]
		}
		layers[newIdx] = l
	}

	recordKeys := canonicalRecordKeys(cc.Records, layerKeys)

	recordOrder := make([]int, len(cc.Records))
	for i := range recordOrder {
		recordOrder[i] = i
	}
	slices.SortStableFunc(recordOrder, func(a, b int) int {
		return cmp.Or(
			cmp.Compare(cc.Records[a].Digest, cc.Records[b].Digest),
			cmp.Compare(recordKeys[a], recordKeys[b]),
		)
	})
	newRecordIndex := make([]int, len(cc.Records))
	var kept []int
	for _, oldIdx := range recordOrder {
		if n := len(kept); n > 0 && recordKeys[kept[n-1]] == recordKeys[oldIdx] {
			newRecordIndex[oldIdx] = n - 1
			continue
		}
		newRecordIndex[oldIdx] = len(kept)
		kept = append(kept, oldIdx)
	}

	records := make([]cacheimporttypes.CacheRecord, len(kept))
	for newIdx, oldIdx := range kept {
		r := cc.Records[oldIdx]

		results := make([]cacheimporttypes.CacheResult, len(r.Results))
		for i, res := range r.Results {
			res.LayerIndex = newLayerIndex[res.LayerIndex]
			results[i] = res
		}
		slices.SortFunc(results, func(a, b cacheimporttypes.CacheResult) int {
			return cmp.Or(cmp.Compare(a.LayerIndex, b.LayerIndex), a.CreatedAt.Compare(b.CreatedAt))
		})
		results = slices.CompactFunc(results, func(a, b cacheimporttypes.CacheResult) bool {
			return a.LayerIndex == b.LayerIndex && a.CreatedAt.Equal(b.CreatedAt)
		})
		if len(results) == 0 {
			results = nil
		}
		r.Results = results

		var chained []cacheimporttypes.ChainedResult
		for _, res := range r.ChainedResults {
			idxs := make([]int, len(res.LayerIndexes))
			for i, idx := range res.LayerIndexes {
				idxs[i] = newLayerIndex[idx]
			}
			chained = append(chained, cacheimporttypes.ChainedResult{LayerIndexes: idxs, CreatedAt: res.CreatedAt})
		}
		slices.SortFunc(chained, func(a, b cacheimporttypes.ChainedResult) int {
			return cmp.Or(slices.Compare(a.LayerIndexes, b.LayerIndexes), a.CreatedAt.Compare(b.CreatedAt))
		})
		r.ChainedResults = chained

		inputs := make([][]cacheimporttypes.CacheInput, len(r.Inputs))
		for i, in := range r.Inputs {
			inputs[i] = make([]cacheimporttypes.CacheInput, len(in))
			for j, link := range in {
				link.LinkIndex = newRecordIndex[link.LinkIndex]
				inputs[i][j] = link
			}
			slices.SortFunc(inputs[i], func(a, b cacheimporttypes.CacheInput) int {
				return cmp.Or(cmp.Compare(a.LinkIndex, b.LinkIndex), cmp.Compare(a.Selector, b.Selector))
			})
			inputs[i] = slices.Compact(inputs[i])
		}
		if len(r.Inputs) == 0 {
			inputs = r.Inputs
		}
		r.Inputs = inputs

		records[newIdx] = r
	}

	cc.Layers = layers
	cc.Records = records
}

// canonicalLayerKeys returns, for each layer, a key made of the blob digests
// of the layer's whole chain, from the base layer to the layer itself.
func canonicalLayerKeys(layers []cacheimporttypes.CacheLayer) []string {
	keys := make([]string, len(layers))
	visiting := make([]bool, len(layers))
	var key func(i int) string
	key = func(i int) string {
		if i < 0 || i >= len(layers) || visiting[i] {
			return ""
		}
		if keys[i] != "" {
			return keys[i]
		}
		visiting[i] = true
		k := layers[i].Blob.String()
		if p := layers[i].ParentIndex; p != -1 {
			k = key(p) + "/" + k
		}
		visiting[i] = false
		keys[i] = k
		return k
	}
	for i := range layers {
		key(i)
	}
	return keys
}

// canonicalRecordKeys returns, for each record, a digest of everything that
// identifies the record independently of its position in the config: its
// digest, its results (as layer chains) and, recursively, its inputs.
func canonicalRecordKeys(records []cacheimporttypes.CacheRecord, layerKeys []string) []digest.Digest {
	layerKey := func(i int) string {
		if i < 0 || i >= len(layerKeys) {
			return ""
		}
		return layerKeys[i]
	}

	keys := make([]digest.Digest, len(records))
	visiting := make([]bool, len(records))
	var key func(i int) digest.Digest
	key = func(i int) digest.Digest {
		if i < 0 || i >= len(records) || visiting[i] {
			return ""
		}
		if keys[i] != "" {
			return keys[i]
		}
		visiting[i] = true
		defer func() { visiting[i] = false }()

		r := records[i]
		var b strings.Builder
		fmt.Fprintf(&b, "digest %s\n", r.Digest)

		results := make([]string, 0, len(r.Results))
		for _, res := range r.Results {
			results = append(results, fmt.Sprintf("result %s %s\n", layerKey(res.LayerIndex), res.CreatedAt.UTC().Format(time.RFC3339Nano)))
		}
		slices.Sort(results)
		for _, s := range results {
			b.WriteString(s)
		}

		chained := make([]string, 0, len(r.ChainedResults))
		for _, res := range r.ChainedResults {
			ls := make([]string, len(res.LayerIndexes))
			for j, idx := range res.LayerIndexes {
				ls[j] = layerKey(idx)
			}
			chained = append(chained, fmt.Sprintf("chained %s %s\n", strings.Join(ls, ","), res.CreatedAt.UTC().Format(time.RFC3339Nano)))
		}
		slices.Sort(chained)
		for _, s := range chained {
			b.WriteString(s)
		}

		for idx, in := range r.Inputs {
			links := make([]string, 0, len(in))
			for _, link := range in {
				links = append(links, fmt.Sprintf("%q %s", link.Selector, key(link.LinkIndex)))
			}
			slices.Sort(links)
			links = slices.Compact(links)
			fmt.Fprintf(&b, "input %d %s\n", idx, strings.Join(links, ","))
		}

		keys[i] = digest.FromString(b.String())
		return keys[i]
	}
	for i := range records {
		key(i)
	}
	return keys
}

func outputKey(dgst digest.Digest, idx int) digest.Digest {
	return digest.FromBytes(fmt.Appendf(nil, "%s@%d", dgst, idx))
}

type nlink struct {
	dgst     digest.Digest
	input    int
	selector string
}

type marshalState struct {
	layers      []cacheimporttypes.CacheLayer
	chainsByID  map[string]int
	descriptors DescriptorProvider

	records       []cacheimporttypes.CacheRecord
	recordsByItem map[*item]int
}

func marshalRemote(ctx context.Context, r *solver.Remote, state *marshalState) string {
	if len(r.Descriptors) == 0 {
		return ""
	}

	if r.Provider != nil {
		for _, d := range r.Descriptors {
			if _, err := r.Provider.Info(ctx, d.Digest); err != nil && !cerrdefs.IsNotImplemented(err) {
				bklog.G(ctx).WithError(err).Warnf("skipping cache result: blob %s is not available", d.Digest)
				return ""
			}
		}
	}

	var parentID string
	for _, desc := range r.Descriptors {
		if desc.MediaType != "" {
			desc = compression.ConvertAllLayerMediaTypes(ctx, true, desc)[0]
		}

		state.descriptors[desc.Digest] = DescriptorProviderPair{
			Descriptor: desc,
			Provider:   r.Provider,
		}

		id := desc.Digest.String() + parentID

		if _, ok := state.chainsByID[id]; !ok {
			state.chainsByID[id] = len(state.layers)
			l := cacheimporttypes.CacheLayer{
				Blob:        desc.Digest,
				ParentIndex: -1,
			}
			if parentID != "" {
				l.ParentIndex = state.chainsByID[parentID]
			}
			state.layers = append(state.layers, l)
		}
		parentID = id
	}
	return parentID
}

func marshalItem(ctx context.Context, it *item, state *marshalState) error {
	if _, ok := state.recordsByItem[it]; ok {
		return nil
	}
	state.recordsByItem[it] = -1

	rec := cacheimporttypes.CacheRecord{
		Digest: it.dgst,
		Inputs: make([][]cacheimporttypes.CacheInput, len(it.parents)),
	}

	for i, m := range it.parents {
		for l := range m {
			if err := marshalItem(ctx, l.src, state); err != nil {
				return err
			}
			idx, ok := state.recordsByItem[l.src]
			if !ok {
				return errors.Errorf("invalid source record: %v", l.src)
			}
			if idx == -1 {
				continue
			}
			rec.Inputs[i] = append(rec.Inputs[i], cacheimporttypes.CacheInput{
				Selector:  l.selector,
				LinkIndex: idx,
			})
		}
	}

	if res := it.bestResult(); res != nil {
		id := marshalRemote(ctx, res.Result, state)
		if id != "" {
			idx, ok := state.chainsByID[id]
			if !ok {
				return errors.New("parent chainid not found")
			}
			rec.Results = append(rec.Results, cacheimporttypes.CacheResult{LayerIndex: idx, CreatedAt: res.CreatedAt})
		}
	}

	state.recordsByItem[it] = len(state.records)
	state.records = append(state.records, rec)
	return nil
}

func isSubRemote(sub, main solver.Remote) bool {
	if len(sub.Descriptors) > len(main.Descriptors) {
		return false
	}
	for i := range sub.Descriptors {
		if sub.Descriptors[i].Digest != main.Descriptors[i].Digest {
			return false
		}
	}
	return true
}
