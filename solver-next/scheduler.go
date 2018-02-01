package solver

import (
	"context"
	"sync"

	"github.com/moby/buildkit/solver-next/internal/pipe"
	"github.com/moby/buildkit/util/cond"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const debugScheduler = false // TODO: replace with logs in build trace

func NewScheduler(ef EdgeFactory) *Scheduler {
	s := &Scheduler{
		waitq:    map[*edge]struct{}{},
		incoming: map[*edge][]*edgePipe{},
		outgoing: map[*edge][]*edgePipe{},

		stopped: make(chan struct{}),
		closed:  make(chan struct{}),

		ef: ef,
	}
	s.cond = cond.NewStatefulCond(&s.mu)

	go s.loop()

	return s
}

type Scheduler struct {
	cond *cond.StatefulCond
	mu   sync.Mutex
	muQ  sync.Mutex

	ef EdgeFactory

	waitq       map[*edge]struct{}
	stopped     chan struct{}
	stoppedOnce sync.Once
	closed      chan struct{}

	incoming map[*edge][]*edgePipe
	outgoing map[*edge][]*edgePipe
}

func (s *Scheduler) Stop() {
	s.stoppedOnce.Do(func() {
		close(s.stopped)
	})
	<-s.closed
}

func (s *Scheduler) loop() {
	defer func() {
		close(s.closed)
	}()

	go func() {
		<-s.stopped
		s.mu.Lock()
		s.cond.Signal()
		s.mu.Unlock()
	}()

	s.mu.Lock()
	for {
		select {
		case <-s.stopped:
			s.mu.Unlock()
			return
		default:
		}
		s.muQ.Lock()
		q := s.waitq
		s.waitq = map[*edge]struct{}{}
		s.muQ.Unlock()
		if len(q) == 0 {
			s.cond.Wait()
			continue
		}

		for e := range q {
			s.dispatch(e)
		}
	}
}

// dispatch schedules an edge to be processed
func (s *Scheduler) dispatch(e *edge) {
	inc := make([]pipe.Sender, len(s.incoming[e]))
	for i, p := range s.incoming[e] {
		inc[i] = p.Sender
	}
	out := make([]pipe.Receiver, len(s.outgoing[e]))
	for i, p := range s.outgoing[e] {
		out[i] = p.Receiver
	}

	e.hasActiveOutgoing = false
	updates := []pipe.Receiver{}
	for _, p := range out {
		if ok := p.Receive(); ok {
			updates = append(updates, p)
		}
		if !p.Status().Completed {
			e.hasActiveOutgoing = true
		}
	}

	// unpark the edge
	debugSchedulerPreUnpark(e, inc, updates, out)
	e.unpark(inc, updates, out, &pipeFactory{s: s, e: e})
	debugSchedulerPostUnpark(e, inc)

	// set up new requests that didn't complete/were added by this run
	openIncoming := make([]*edgePipe, 0, len(inc))
	for _, r := range s.incoming[e] {
		if !r.Sender.Status().Completed {
			openIncoming = append(openIncoming, r)
		}
	}
	if len(openIncoming) > 0 {
		s.incoming[e] = openIncoming
	} else {
		delete(s.incoming, e)
	}

	openOutgoing := make([]*edgePipe, 0, len(out))
	for _, r := range s.outgoing[e] {
		if !r.Receiver.Status().Completed {
			openOutgoing = append(openOutgoing, r)
		}
	}
	if len(openOutgoing) > 0 {
		s.outgoing[e] = openOutgoing
	} else {
		delete(s.outgoing, e)
	}

	// if keys changed there might be possiblity for merge with other edge
	if e.keysDidChange {
		origEdge := e.index.LoadOrStore(e, e.cacheMap.Digest, e.edge.Index, e.depKeys())
		if origEdge != nil {
			logrus.Debugf("merging edge %s to %s\n", e.edge.Vertex.Name(), origEdge.edge.Vertex.Name())
			s.mergeTo(origEdge, e)
			s.ef.SetEdge(e.edge, origEdge)
		}
		e.keysDidChange = false
	}

	// validation to avoid deadlocks/resource leaks:
	// TODO: if these start showing up in error reports they can be changed
	// to error the edge instead. They can only appear from algorithm bugs in
	// unpark(), not for any external input.
	if len(openIncoming) > 0 && len(openOutgoing) == 0 {
		panic("invalid dispatch: return leaving incoming open")
	}
	if len(openIncoming) == 0 && len(openOutgoing) > 0 {
		panic("invalid dispatch: return leaving outgoing open")
	}
}

// signal notifies that an edge needs to be processed again
func (s *Scheduler) signal(e *edge) {
	s.muQ.Lock()
	if _, ok := s.waitq[e]; !ok {
		s.waitq[e] = struct{}{}
		s.cond.Signal()
	}
	s.muQ.Unlock()
}

// build evaluates edge into a result
func (s *Scheduler) build(ctx context.Context, edge Edge) (CachedResult, error) {
	s.mu.Lock()
	e := s.ef.GetEdge(edge)
	if e == nil {
		s.mu.Unlock()
		return nil, errors.Errorf("invalid request %v for build", edge)
	}

	wait := make(chan struct{})

	var p *pipe.Pipe
	p = s.newPipe(e, nil, pipe.Request{Payload: &edgeRequest{desiredState: edgeStatusComplete}})
	p.OnSendCompletion = func() {
		p.Receiver.Receive()
		if p.Receiver.Status().Completed {
			close(wait)
		}
	}
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		<-ctx.Done()
		p.Receiver.Cancel()
	}()

	<-wait

	if err := p.Receiver.Status().Err; err != nil {
		return nil, err
	}
	return p.Receiver.Status().Value.(*edgeState).result, nil
}

// newPipe creates a new request pipe between two edges
func (s *Scheduler) newPipe(target, from *edge, req pipe.Request) *pipe.Pipe {
	p := &edgePipe{
		Pipe:   pipe.New(req),
		Target: target,
		From:   from,
	}

	s.signal(target)
	if from != nil {
		p.OnSendCompletion = func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			s.signal(p.From)
		}
		s.outgoing[from] = append(s.outgoing[from], p)
	}
	s.incoming[target] = append(s.incoming[target], p)
	p.OnReceiveCompletion = func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		s.signal(p.Target)
	}
	return p.Pipe
}

// newRequestWithFunc creates a new request pipe that invokes a async function
func (s *Scheduler) newRequestWithFunc(e *edge, f func(context.Context) (interface{}, error)) pipe.Receiver {
	pp, start := pipe.NewWithFunction(f)
	p := &edgePipe{
		Pipe: pp,
		From: e,
	}
	p.OnSendCompletion = func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		s.signal(p.From)
	}
	s.outgoing[e] = append(s.outgoing[e], p)
	go start()
	return p.Receiver
}

// mergeTo merges the state from one edge to another. source edge is discarded.
func (s *Scheduler) mergeTo(target, src *edge) {
	for _, inc := range s.incoming[src] {
		inc.mu.Lock()
		inc.Target = target
		s.incoming[target] = append(s.incoming[target], inc)
		inc.mu.Unlock()
	}

	for _, out := range s.outgoing[src] {
		out.mu.Lock()
		out.From = target
		s.outgoing[target] = append(s.outgoing[target], out)
		out.mu.Unlock()
		out.Receiver.Cancel()
	}

	delete(s.incoming, src)
	delete(s.outgoing, src)
	s.signal(target)

	// TODO(tonistiigi): merge cache providers
	// TODO(tonistiigi): check ignore-cache compat before merge
}

// EdgeFactory allows access to the edges from a shared graph
type EdgeFactory interface {
	GetEdge(Edge) *edge
	SetEdge(Edge, *edge)
}

type pipeFactory struct {
	e *edge
	s *Scheduler
}

func (pf *pipeFactory) NewInputRequest(ee Edge, req *edgeRequest) pipe.Receiver {
	target := pf.s.ef.GetEdge(ee)
	if target == nil {
		panic("failed to get edge") // TODO: return errored pipe
	}
	p := pf.s.newPipe(target, pf.e, pipe.Request{Payload: req})
	if debugScheduler {
		logrus.Debugf("> newPipe %s %p desiredState=%s", ee.Vertex.Name(), p, req.desiredState)
	}
	return p.Receiver
}

func (pf *pipeFactory) NewFuncRequest(f func(context.Context) (interface{}, error)) pipe.Receiver {
	p := pf.s.newRequestWithFunc(pf.e, f)
	if debugScheduler {
		logrus.Debugf("> newFunc %p", p)
	}
	return p
}

func debugSchedulerPreUnpark(e *edge, inc []pipe.Sender, updates, allPipes []pipe.Receiver) {
	if !debugScheduler {
		return
	}
	logrus.Debugf(">> unpark %s req=%d upt=%d out=%d state=%s", e.edge.Vertex.Name(), len(inc), len(updates), len(allPipes), e.state)

	for i, dep := range e.deps {
		des := edgeStatusInitial
		if dep.req != nil {
			des = dep.req.Request().(*edgeRequest).desiredState
		}
		logrus.Debugf(":: dep%d %s state=%s des=%s", i, e.edge.Vertex.Inputs()[i].Vertex.Name(), dep.state, des)
	}

	for i, in := range inc {
		req := in.Request()
		logrus.Debugf("> incoming-%d: %p dstate=%s canceled=%v", i, in, req.Payload.(*edgeRequest).desiredState, req.Canceled)
	}

	for i, up := range updates {
		if up == e.cacheMapReq {
			logrus.Debugf("> update-%d: %p cacheMapReq complete=%v", i, up, up.Status().Completed)
		} else if up == e.execReq {
			logrus.Debugf("> update-%d: %p execReq complete=%v", i, up, up.Status().Completed)
		} else {
			st, ok := up.Status().Value.(*edgeState)
			if ok {
				index := -1
				if dep, ok := e.depRequests[up]; ok {
					index = int(dep.index)
				}
				logrus.Debugf("> update-%d: %p input-%d keys=%d state=%s", i, up, index, len(st.keys), st.state)
			}
		}
	}
}

func debugSchedulerPostUnpark(e *edge, inc []pipe.Sender) {
	if !debugScheduler {
		return
	}
	for i, in := range inc {
		logrus.Debugf("< incoming-%d: %p completed=%v", i, in, in.Status().Completed)
	}
	logrus.Debugf("<< unpark %s\n", e.edge.Vertex.Name())
}
