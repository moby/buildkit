package solver

import (
	"context"
	"sync"

	"github.com/moby/buildkit/solver-next/internal/pipe"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type edgeStatusType int

const (
	edgeStatusInitial edgeStatusType = iota
	edgeStatusCacheFast
	edgeStatusCacheSlow
	edgeStatusComplete
)

func (t edgeStatusType) String() string {
	return []string{"initial", "cache-fast", "cache-slow", "complete"}[t]
}

func newEdge(ed Edge, op activeOp, index *EdgeIndex) *edge {
	e := &edge{
		edge:         ed,
		op:           op,
		depRequests:  map[pipe.Receiver]*dep{},
		cacheRecords: map[string]*CacheRecord{},
		index:        index,
	}
	return e
}

type edge struct {
	edge Edge
	op   activeOp

	edgeState
	depRequests map[pipe.Receiver]*dep
	deps        []*dep

	cacheMapReq  pipe.Receiver
	execReq      pipe.Receiver
	err          error
	cacheRecords map[string]*CacheRecord

	noCacheMatchPossible      bool
	allDepsCompletedCacheFast bool
	allDepsCompletedCacheSlow bool
	allDepsStateCacheSlow     bool
	allDepsCompleted          bool
	hasActiveOutgoing         bool

	releaserCount int
	keysDidChange bool
	index         *EdgeIndex
}

// dep holds state for a dependant edge
type dep struct {
	req pipe.Receiver
	edgeState
	index             Index
	cacheRecords      map[string]*CacheRecord
	desiredState      edgeStatusType
	e                 *edge
	slowCacheReq      pipe.Receiver // TODO: reuse req
	slowCacheComplete bool
	slowCacheFoundKey bool
	slowCacheKey      *ExportableCacheKey
	err               error
}

func newDep(i Index) *dep {
	return &dep{index: i, cacheRecords: map[string]*CacheRecord{}}
}

type edgePipe struct {
	*pipe.Pipe
	From, Target *edge
	mu           sync.Mutex
}

type edgeState struct {
	state    edgeStatusType
	result   *SharedCachedResult
	cacheMap *CacheMap
	keys     []ExportableCacheKey
}

func isEqualState(s1, s2 edgeState) bool {
	if s1.state != s2.state || s1.result != s2.result || s1.cacheMap != s2.cacheMap || len(s1.keys) != len(s2.keys) {
		return false
	}
	return true
}

type edgeRequest struct {
	desiredState edgeStatusType
	currentState edgeState
	currentKeys  int
}

// incrementReferenceCount increases the number of times release needs to be
// called to release the edge. Called on merging edges.
func (e *edge) incrementReferenceCount() {
	e.releaserCount += 1
}

// release releases the edge resources
func (e *edge) release() {
	if e.releaserCount > 0 {
		e.releaserCount--
		return
	}
	e.index.Release(e)
	if e.result != nil {
		e.result.Release(context.TODO())
	}
}

// commitOptions returns parameters for the op execution
func (e *edge) commitOptions() (CacheKey, []CachedResult) {
	if e.deps == nil {
		return NewCacheKey(e.cacheMap.Digest, e.edge.Index, nil), nil
	}

	inputs := make([]CacheKeyWithSelector, len(e.deps))
	results := make([]CachedResult, len(e.deps))
	for i, dep := range e.deps {
		inputs[i] = CacheKeyWithSelector{CacheKey: dep.result.CacheKey(), Selector: e.cacheMap.Deps[i].Selector}
		if dep.slowCacheKey != nil {
			inputs[i] = CacheKeyWithSelector{CacheKey: *dep.slowCacheKey}
		}
		results[i] = dep.result
	}
	return NewCacheKey(e.cacheMap.Digest, e.edge.Index, inputs), results
}

// isComplete returns true if edge state is final and will never change
func (e *edge) isComplete() bool {
	return e.err != nil || e.result != nil
}

// finishIncoming finalizes the incoming pipe request
func (e *edge) finishIncoming(req pipe.Sender) {
	err := e.err
	if req.Request().Canceled && err == nil {
		err = context.Canceled
	}
	if debugScheduler {
		logrus.Debugf("finishIncoming %s %v %#v desired=%s", e.edge.Vertex.Name(), err, e.edgeState, req.Request().Payload.(*edgeRequest).desiredState)
	}
	req.Finalize(&e.edgeState, err)
}

// updateIncoming updates the current value of incoming pipe request
func (e *edge) updateIncoming(req pipe.Sender) {
	req.Update(&e.edgeState)
}

// probeCache is called with unprocessed cache keys for dependency
// if the key could match the edge, the cacheRecords for dependency are filled
func (e *edge) probeCache(d *dep, keys []ExportableCacheKey) bool {
	if len(keys) == 0 {
		return false
	}
	if e.op.IgnoreCache() {
		return false
	}
	records, err := e.op.Cache().Query(keys, d.index, e.cacheMap.Digest, e.edge.Index, e.cacheMap.Deps[d.index].Selector)
	if err != nil {
		e.err = errors.Wrap(err, "error on cache query")
	}
	for _, r := range records {
		if _, ok := d.cacheRecords[r.ID]; !ok {
			d.cacheRecords[r.ID] = r
		}
	}
	return len(records) > 0
}

// checkDepMatchPossible checks if any cache matches are possible past this point
func (e *edge) checkDepMatchPossible(dep *dep) {
	depHasSlowCache := e.cacheMap.Deps[dep.index].ComputeDigestFunc != nil
	if !e.noCacheMatchPossible && (((!dep.slowCacheFoundKey && dep.slowCacheComplete && depHasSlowCache) || (!depHasSlowCache && dep.state >= edgeStatusCacheSlow)) && len(dep.keys) == 0) {
		e.noCacheMatchPossible = true
	}
}

// slowCacheFunc returns the result based cache func for dependency if it exists
func (e *edge) slowCacheFunc(dep *dep) ResultBasedCacheFunc {
	if e.cacheMap == nil {
		return nil
	}
	return e.cacheMap.Deps[int(dep.index)].ComputeDigestFunc
}

// allDepsHaveKeys checks if all dependencies have at least one key. used for
// determining if there is enough data for combining cache key for edge
func (e *edge) allDepsHaveKeys() bool {
	if e.cacheMap == nil {
		return false
	}
	for _, d := range e.deps {
		if len(d.keys) == 0 && d.slowCacheKey == nil {
			return false
		}
	}
	return true
}

// depKeys returns all current dependency cache keys
func (e *edge) depKeys() [][]CacheKey {
	keys := make([][]CacheKey, len(e.deps))
	for i, d := range e.deps {
		for _, k := range d.keys {
			keys[i] = append(keys[i], k.CacheKey)
		}
		if d.result != nil {
			keys[i] = append(keys[i], d.result.CacheKey())
		}
		if d.slowCacheKey != nil {
			keys[i] = append(keys[i], d.slowCacheKey)
		}
	}
	return keys
}

// slow cache keys can be computed in 2 phases if there are multiple deps.
// first evaluate ones that didn't match any definition based keys
func (e *edge) skipPhase2SlowCache(dep *dep) bool {
	isPhase1 := false
	for _, dep := range e.deps {
		if !dep.slowCacheComplete && e.slowCacheFunc(dep) != nil && len(dep.cacheRecords) == 0 {
			isPhase1 = true
			break
		}
	}

	if isPhase1 && !dep.slowCacheComplete && e.slowCacheFunc(dep) != nil && len(dep.cacheRecords) > 0 {
		return true
	}
	return false
}

func (e *edge) skipPhase2FastCache(dep *dep) bool {
	isPhase1 := false
	for _, dep := range e.deps {
		if e.cacheMap == nil || len(dep.cacheRecords) == 0 && ((!dep.slowCacheComplete && e.slowCacheFunc(dep) != nil) || (dep.state < edgeStatusComplete && e.slowCacheFunc(dep) == nil)) {
			isPhase1 = true
			break
		}
	}

	if isPhase1 && len(dep.cacheRecords) > 0 {
		return true
	}
	return false
}

// unpark is called by the scheduler with incoming requests and updates for
// previous calls.
// To avoid deadlocks and resource leaks this function needs to follow
// following rules:
// 1) this function needs to return unclosed outgoing requests if some incoming
//    requests were not completed
// 2) this function may not return outgoing requests if it has completed all
//    incoming requests
func (e *edge) unpark(incoming []pipe.Sender, updates, allPipes []pipe.Receiver, f *pipeFactory) {
	// process all incoming changes
	depChanged := false
	for _, upt := range updates {
		if changed := e.processUpdate(upt); changed {
			depChanged = true
		}
	}

	if depChanged {
		// the dep responses had changes. need to reevaluate edge state
		e.recalcCurrentState()
	}

	desiredState, done := e.respondToIncoming(incoming, allPipes)
	if done {
		return
	}

	// set up new outgoing requests if needed
	if e.cacheMapReq == nil {
		e.cacheMapReq = f.NewFuncRequest(func(ctx context.Context) (interface{}, error) {
			return e.op.CacheMap(ctx)
		})
	}

	// execute op
	if e.execReq == nil && desiredState == edgeStatusComplete {
		if ok := e.execIfPossible(f); ok {
			return
		}
	}

	if e.execReq == nil {
		e.createInputRequests(desiredState, f)
	}

}

// processUpdate is called by unpark for every updated pipe request
func (e *edge) processUpdate(upt pipe.Receiver) (depChanged bool) {
	// response for cachemap request
	if upt == e.cacheMapReq && upt.Status().Completed {
		if err := upt.Status().Err; err != nil {
			e.cacheMapReq = nil
			if !upt.Status().Canceled && e.err == nil {
				e.err = err
			}
		} else {
			e.cacheMap = upt.Status().Value.(*CacheMap)
			if len(e.deps) == 0 {
				if !e.op.IgnoreCache() {
					records, err := e.op.Cache().Query(nil, 0, e.cacheMap.Digest, e.edge.Index, "")
					if err != nil {
						logrus.Error(errors.Wrap(err, "invalid query response")) // make the build fail for this error
					} else {
						for _, r := range records {
							if r.Loadable {
								e.cacheRecords[r.ID] = r
							}
						}
						if len(records) > 0 {
							e.keys = append(e.keys, records[0].CacheKey)
						}
					}
				}
				if e.allDepsHaveKeys() {
					e.keysDidChange = true
				}
				e.state = edgeStatusCacheSlow
			}
			// probe keys that were loaded before cache map
			for _, dep := range e.deps {
				e.probeCache(dep, dep.keys)
				e.checkDepMatchPossible(dep)
			}
		}
		return true
	}

	// response for exec request
	if upt == e.execReq && upt.Status().Completed {
		if err := upt.Status().Err; err != nil {
			e.execReq = nil
			if !upt.Status().Canceled && e.err == nil {
				e.err = err
			}
		} else {
			e.result = NewSharedCachedResult(upt.Status().Value.(CachedResult))
			e.state = edgeStatusComplete
		}
		return true
	}

	// response for requests to dependencies
	if dep, ok := e.depRequests[upt]; ok { // TODO: ignore canceled
		if err := upt.Status().Err; !upt.Status().Canceled && upt.Status().Completed && err != nil {
			if e.err == nil {
				e.err = err
			}
			dep.err = err
		}

		state := upt.Status().Value.(*edgeState)

		if len(dep.keys) < len(state.keys) {
			newKeys := state.keys[len(dep.keys):]

			if e.cacheMap != nil {
				e.probeCache(dep, newKeys)
				if e.allDepsHaveKeys() {
					e.keysDidChange = true
				}
			}
			depChanged = true
		}
		if dep.state != edgeStatusComplete && state.state == edgeStatusComplete {
			e.keysDidChange = true
		}

		recheck := state.state != dep.state

		dep.edgeState = *state

		if recheck && e.cacheMap != nil {
			e.checkDepMatchPossible(dep)
			depChanged = true
		}

		return
	}

	// response for result based cache function
	for i, dep := range e.deps {
		if upt == dep.slowCacheReq && upt.Status().Completed {
			if err := upt.Status().Err; err != nil {
				dep.slowCacheReq = nil
				if !upt.Status().Canceled && e.err == nil {
					e.err = upt.Status().Err
				}
			} else if !dep.slowCacheComplete {
				k := NewCacheKey(upt.Status().Value.(digest.Digest), -1, nil)
				ck := NewCacheKey("", 0, []CacheKeyWithSelector{
					{CacheKey: dep.result.CacheKey(), Selector: e.cacheMap.Deps[i].Selector},
					{CacheKey: ExportableCacheKey{CacheKey: k, Exporter: &emptyExporter{k}}, Selector: NoSelector},
				})
				dep.slowCacheKey = &ExportableCacheKey{ck, &emptyExporter{ck}}
				dep.slowCacheFoundKey = e.probeCache(dep, []ExportableCacheKey{*dep.slowCacheKey})
				dep.slowCacheComplete = true
				e.keysDidChange = true
				e.checkDepMatchPossible(dep) // not matching key here doesn't set nocachematch possible to true
			}
			return true
		}
	}

	return
}

// recalcCurrentState is called by unpark to recompute internal state after
// the state of dependencies has changed
func (e *edge) recalcCurrentState() {
	// TODO: fast pass to detect incomplete results
	newRecords := map[string]*CacheRecord{}

	for i, dep := range e.deps {
		if i == 0 {
			for key, r := range dep.cacheRecords {
				if _, ok := e.cacheRecords[key]; ok {
					continue
				}
				newRecords[key] = r
			}
		} else {
			for key := range newRecords {
				if _, ok := dep.cacheRecords[key]; !ok {
					delete(newRecords, key)
				}
			}
		}
		if len(newRecords) == 0 {
			break
		}
	}

	for k, r := range newRecords {
		e.keys = append(e.keys, r.CacheKey)
		if r.Loadable {
			e.cacheRecords[k] = r
		}
	}

	// detect lower/upper bound for current state
	allDepsCompletedCacheFast := e.cacheMap != nil
	allDepsCompletedCacheSlow := e.cacheMap != nil
	allDepsStateCacheSlow := true
	allDepsCompleted := true
	stLow := edgeStatusInitial    // minimal possible state
	stHigh := edgeStatusCacheSlow // maximum possible state
	if e.cacheMap != nil {
		for _, dep := range e.deps {
			isSlowIncomplete := e.slowCacheFunc(dep) != nil && (dep.state == edgeStatusCacheSlow || (dep.state == edgeStatusComplete && !dep.slowCacheComplete))

			if dep.state > stLow && len(dep.cacheRecords) == 0 && !isSlowIncomplete {
				stLow = dep.state
				if stLow > edgeStatusCacheSlow {
					stLow = edgeStatusCacheSlow
				}
			}
			effectiveState := dep.state
			if dep.state == edgeStatusCacheSlow && isSlowIncomplete {
				effectiveState = edgeStatusCacheFast
			}
			if dep.state == edgeStatusComplete && isSlowIncomplete {
				effectiveState = edgeStatusCacheFast
			}
			if effectiveState < stHigh {
				stHigh = effectiveState
			}
			if isSlowIncomplete || dep.state < edgeStatusComplete {
				allDepsCompleted = false
			}
			if dep.state < edgeStatusCacheFast {
				allDepsCompletedCacheFast = false
			}
			if isSlowIncomplete || dep.state < edgeStatusCacheSlow {
				allDepsCompletedCacheSlow = false
			}
			if dep.state < edgeStatusCacheSlow && len(dep.keys) == 0 {
				allDepsStateCacheSlow = false
			}
		}
		if stLow > e.state {
			e.state = stLow
		}
		if stHigh > e.state {
			e.state = stHigh
		}

		e.allDepsCompletedCacheFast = allDepsCompletedCacheFast
		e.allDepsCompletedCacheSlow = allDepsCompletedCacheSlow
		e.allDepsStateCacheSlow = allDepsStateCacheSlow
		e.allDepsCompleted = allDepsCompleted
	}
}

// respondToIncoming responds to all incoming requests. completing or
// updating them when possible
func (e *edge) respondToIncoming(incoming []pipe.Sender, allPipes []pipe.Receiver) (edgeStatusType, bool) {
	// detect the result state for the requests
	allIncomingCanComplete := true
	desiredState := e.state
	allCanceled := true

	// check incoming requests
	// check if all requests can be either answered or canceled
	if !e.isComplete() {
		for _, req := range incoming {
			if !req.Request().Canceled {
				allCanceled = false
				if r := req.Request().Payload.(*edgeRequest); desiredState < r.desiredState {
					desiredState = r.desiredState
					if e.hasActiveOutgoing || r.desiredState == edgeStatusComplete || r.currentKeys == len(e.keys) {
						allIncomingCanComplete = false
					}
				}
			}
		}
	}

	// do not set allIncomingCanComplete if active ongoing can modify the state
	if !allCanceled && e.state < edgeStatusComplete && len(e.keys) == 0 && e.hasActiveOutgoing {
		allIncomingCanComplete = false
	}

	if debugScheduler {
		logrus.Debugf("status state=%s cancomplete=%v hasouts=%v noPossibleCache=%v depsCacheFast=%v keys=%d cacheRecords=%d", e.state, allIncomingCanComplete, e.hasActiveOutgoing, e.noCacheMatchPossible, e.allDepsCompletedCacheFast, len(e.keys), len(e.cacheRecords))
	}

	if allIncomingCanComplete && e.hasActiveOutgoing {
		// cancel all current requests
		for _, p := range allPipes {
			p.Cancel()
		}

		// can close all but one requests
		var leaveOpen pipe.Sender
		for _, req := range incoming {
			if !req.Request().Canceled {
				leaveOpen = req
				break
			}
		}
		for _, req := range incoming {
			if leaveOpen == nil || leaveOpen == req {
				leaveOpen = req
				continue
			}
			e.finishIncoming(req)
		}
		return desiredState, true
	}

	// can complete, finish and return
	if allIncomingCanComplete && !e.hasActiveOutgoing {
		for _, req := range incoming {
			e.finishIncoming(req)
		}
		return desiredState, true
	}

	// update incoming based on current state
	for _, req := range incoming {
		r := req.Request().Payload.(*edgeRequest)
		if req.Request().Canceled {
			e.finishIncoming(req)
		} else if !e.hasActiveOutgoing && e.state >= r.desiredState {
			e.finishIncoming(req)
		} else if !isEqualState(r.currentState, e.edgeState) && !req.Request().Canceled {
			e.updateIncoming(req)
		}
	}
	return desiredState, false
}

// createInputRequests creates new requests for dependencies or async functions
// that need to complete to continue processing the edge
func (e *edge) createInputRequests(desiredState edgeStatusType, f *pipeFactory) {
	// initialize deps state
	if e.deps == nil {
		e.depRequests = make(map[pipe.Receiver]*dep)
		e.deps = make([]*dep, 0, len(e.edge.Vertex.Inputs()))
		for i := range e.edge.Vertex.Inputs() {
			e.deps = append(e.deps, newDep(Index(i)))
		}
	}

	// cycle all dependencies. set up outgoing requests if needed
	for _, dep := range e.deps {
		desiredStateDep := dep.state

		if e.noCacheMatchPossible {
			desiredStateDep = edgeStatusComplete
		} else if dep.state == edgeStatusInitial && desiredState > dep.state {
			desiredStateDep = edgeStatusCacheFast
		} else if dep.state == edgeStatusCacheFast && desiredState > dep.state {
			// wait all deps to complete cache fast before continuing with slow cache
			if (e.allDepsCompletedCacheFast && len(e.keys) == 0) || len(dep.keys) == 0 || e.allDepsHaveKeys() {
				if !e.skipPhase2FastCache(dep) {
					desiredStateDep = edgeStatusCacheSlow
				}
			}
		} else if dep.state == edgeStatusCacheSlow && desiredState == edgeStatusComplete {
			// if all deps have completed cache-slow or content based cache for input is available
			if (len(dep.keys) == 0 || e.allDepsCompletedCacheSlow || (!e.skipPhase2FastCache(dep) && e.slowCacheFunc(dep) != nil)) && (len(e.cacheRecords) == 0) {
				if len(dep.keys) == 0 || !e.skipPhase2SlowCache(dep) && e.allDepsStateCacheSlow {
					desiredStateDep = edgeStatusComplete
				}
			}
		} else if dep.state == edgeStatusCacheSlow && e.slowCacheFunc(dep) != nil && desiredState == edgeStatusCacheSlow {
			if len(dep.keys) == 0 || !e.skipPhase2SlowCache(dep) && e.allDepsStateCacheSlow {
				desiredStateDep = edgeStatusComplete
			}
		}

		// outgoing request is needed
		if dep.state < desiredStateDep {
			addNew := true
			if dep.req != nil && !dep.req.Status().Completed {
				if dep.req.Request().(*edgeRequest).desiredState != desiredStateDep {
					dep.req.Cancel()
				} else {
					addNew = false
				}
			}
			if addNew {
				req := f.NewInputRequest(e.edge.Vertex.Inputs()[int(dep.index)], &edgeRequest{
					currentState: dep.edgeState,
					desiredState: desiredStateDep,
					currentKeys:  len(dep.keys),
				})
				e.depRequests[req] = dep
				dep.req = req
			}
		} else if dep.req != nil && !dep.req.Status().Completed {
			dep.req.Cancel()
		}

		// initialize function to compute cache key based on dependency result
		if dep.state == edgeStatusComplete && dep.slowCacheReq == nil && e.slowCacheFunc(dep) != nil && e.cacheMap != nil {
			fn := e.slowCacheFunc(dep)
			res := dep.result
			func(fn ResultBasedCacheFunc, res Result, index Index) {
				dep.slowCacheReq = f.NewFuncRequest(func(ctx context.Context) (interface{}, error) {
					return e.op.CalcSlowCache(ctx, index, fn, res)
				})
			}(fn, res, dep.index)
		}
	}
}

// execIfPossible creates a request for getting the edge result if there is
// enough state
func (e *edge) execIfPossible(f *pipeFactory) bool {
	if len(e.cacheRecords) > 0 {
		if e.keysDidChange {
			e.postpone(f)
			return true
		}
		e.execReq = f.NewFuncRequest(e.loadCache)
		for req := range e.depRequests {
			req.Cancel()
		}
		return true
	} else if e.allDepsCompleted {
		if e.keysDidChange {
			e.postpone(f)
			return true
		}
		e.execReq = f.NewFuncRequest(e.execOp)
		return true
	}
	return false
}

// postpone delays exec to next unpark invocation if we have unprocessed keys
func (e *edge) postpone(f *pipeFactory) {
	f.NewFuncRequest(func(context.Context) (interface{}, error) {
		return nil, nil
	})
}

// loadCache creates a request to load edge result from cache
func (e *edge) loadCache(ctx context.Context) (interface{}, error) {
	var rec *CacheRecord

	for _, r := range e.cacheRecords {
		if !r.Loadable {
			continue
		}
		if rec == nil || rec.CreatedAt.Before(r.CreatedAt) || (rec.CreatedAt.Equal(r.CreatedAt) && rec.Priority < r.Priority) {
			rec = r
		}
	}
	var exp Exporter
	if len(e.deps) > 0 {
		for _, dep := range e.deps {
			if exp == nil {
				exp = dep.cacheRecords[rec.ID].CacheKey
			} else {
				exp = &mergedExporter{exp, dep.cacheRecords[rec.ID].CacheKey}
			}
		}
	} else {
		exp = rec.CacheKey
	}
	logrus.Debugf("load cache for %s with %s", e.edge.Vertex.Name(), rec.ID)
	res, err := e.op.Cache().Load(ctx, rec)
	if err != nil {
		return nil, err
	}
	return NewCachedResult(res, rec.CacheKey, exp), nil
}

// execOp creates a request to execute the vertex operation
func (e *edge) execOp(ctx context.Context) (interface{}, error) {
	cacheKey, inputs := e.commitOptions()
	results, err := e.op.Exec(ctx, toResultSlice(inputs))
	if err != nil {
		return nil, err
	}

	index := e.edge.Index
	if len(results) <= int(index) {
		return nil, errors.Errorf("invalid response from exec need %d index but %d results received", index, len(results))
	}

	res := results[int(index)]

	for i := range results {
		if i != int(index) {
			go results[i].Release(context.TODO())
		}
	}
	ck, err := e.op.Cache().Save(cacheKey, res)
	if err != nil {
		return nil, err
	}
	return NewCachedResult(res, ck, ck), nil
}

func toResultSlice(cres []CachedResult) (out []Result) {
	out = make([]Result, len(cres))
	for i := range cres {
		out[i] = cres[i].(Result)
	}
	return out
}

type emptyExporter struct {
	CacheKey
}

func (e *emptyExporter) Export(ctx context.Context, m map[digest.Digest]*ExportRecord, fn func(context.Context, Result) (*Remote, error)) (*ExportRecord, error) {

	id := getUniqueID(e.CacheKey)
	rec, ok := m[id]
	if !ok {
		rec = &ExportRecord{Digest: id, Links: map[CacheLink]struct{}{}}
		m[id] = rec
	}

	deps := e.CacheKey.Deps()

	for i, dep := range deps {
		r, err := dep.CacheKey.Export(ctx, m, fn)
		if err != nil {
			return nil, err
		}
		link := CacheLink{
			Source:   r.Digest,
			Input:    Index(i),
			Output:   e.Output(),
			Base:     e.Digest(),
			Selector: dep.Selector,
		}
		rec.Links[link] = struct{}{}
	}

	if len(deps) == 0 {
		m[id].Links[CacheLink{
			Output: e.Output(),
			Base:   e.Digest(),
		}] = struct{}{}
	}

	return rec, nil
}

type mergedExporter struct {
	e1, e2 Exporter
}

func (e *mergedExporter) Export(ctx context.Context, m map[digest.Digest]*ExportRecord, fn func(context.Context, Result) (*Remote, error)) (*ExportRecord, error) {

	_, err := e.e1.Export(ctx, m, fn)
	if err != nil {
		return nil, err
	}

	r, err := e.e2.Export(ctx, m, fn)
	if err != nil {
		return nil, err
	}

	return r, nil
}
