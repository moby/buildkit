package solver

import (
	"context"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/progress"

	digest "github.com/opencontainers/go-digest"
)

type CacheOpts map[interface{}]interface{}

type progressKey struct{}

type cacheOptGetterKey struct{}

func CacheOptGetterOf(ctx context.Context) func(includeAncestors bool, keys ...interface{}) map[interface{}]interface{} {
	if v := ctx.Value(cacheOptGetterKey{}); v != nil {
		if getter, ok := v.(func(includeAncestors bool, keys ...interface{}) map[interface{}]interface{}); ok {
			return getter
		}
	}
	return nil
}

func WithCacheOptGetter(ctx context.Context, getter func(includeAncestors bool, keys ...interface{}) map[interface{}]interface{}) context.Context {
	return context.WithValue(ctx, cacheOptGetterKey{}, getter)
}

func withAncestorCacheOpts(ctx context.Context, start *sharedOp) context.Context {
	return WithCacheOptGetter(ctx, func(includeAncestors bool, keys ...interface{}) map[interface{}]interface{} {
		keySet := make(map[interface{}]struct{})
		for _, k := range keys {
			keySet[k] = struct{}{}
		}
		values := make(map[interface{}]interface{})
		walkAncestors(ctx, start, func(op *sharedOp) bool {
			if op.st.clientVertex.Error != "" {
				// don't use values from cancelled or otherwise error'd vertexes
				return false
			}

			for k := range keySet {
				var v any
				var ok bool

				// check opts set from CacheMap operation
				for _, res := range op.cacheRes {
					if res.Opts == nil {
						continue
					}
					v, ok = res.Opts[k]
					if ok {
						break
					}
				}

				// check opts set during cache load
				if !ok && op.loadCacheOpts != nil {
					v, ok = op.loadCacheOpts[k]
				}

				if ok {
					values[k] = v
					delete(keySet, k)
					if len(keySet) == 0 {
						return true
					}
				}
			}

			return !includeAncestors // stop after the first op unless includeAncestors is true
		})
		return values
	})
}

func walkAncestors(ctx context.Context, start *sharedOp, f func(*sharedOp) bool) {
	stack := [][]*sharedOp{{start}}
	cache := make(map[digest.Digest]struct{})
	for len(stack) > 0 {
		ops := stack[len(stack)-1]
		if len(ops) == 0 {
			stack = stack[:len(stack)-1]
			continue
		}
		op := ops[len(ops)-1]
		stack[len(stack)-1] = ops[:len(ops)-1]
		if op == nil {
			continue
		}
		if _, ok := cache[op.st.origDigest]; ok {
			continue
		}
		cache[op.st.origDigest] = struct{}{}
		if shouldStop := f(op); shouldStop {
			return
		}
		stack = append(stack, []*sharedOp{})
		for _, parentDgst := range op.st.clientVertex.Inputs {
			op.st.solver.mu.RLock()
			parent := op.st.solver.actives[parentDgst]
			op.st.solver.mu.RUnlock()
			if parent == nil {
				bklog.G(ctx).Warnf("parent %q not found in active job list during cache opt search", parentDgst)
				continue
			}
			stack[len(stack)-1] = append(stack[len(stack)-1], parent.op)
		}
	}
}

func ProgressControllerFromContext(ctx context.Context) progress.Controller {
	var pg progress.Controller
	if optGetter := CacheOptGetterOf(ctx); optGetter != nil {
		if kv := optGetter(false, progressKey{}); kv != nil {
			if v, ok := kv[progressKey{}].(progress.Controller); ok {
				pg = v
			}
		}
	}
	return pg
}
