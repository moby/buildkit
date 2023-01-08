package rcmgr

import (
	"fmt"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
)

// resources tracks the current state of resource consumption
type resources struct {
	limit Limit

	nconnsIn, nconnsOut     int
	nstreamsIn, nstreamsOut int
	nfd                     int

	memory int64
}

// A resourceScope can be a DAG, where a downstream node is not allowed to outlive an upstream node
// (ie cannot call Done in the upstream node before the downstream node) and account for resources
// using a linearized parent set.
// A resourceScope can be a span scope, where it has a specific owner; span scopes create a tree rooted
// at the owner (which can be a DAG scope) and can outlive their parents -- this is important because
// span scopes are the main *user* interface for memory management, and the user may call
// Done in a span scope after the system has closed the root of the span tree in some background
// goroutine.
// If we didn't make this distinction we would have a double release problem in that case.
type resourceScope struct {
	sync.Mutex
	done   bool
	refCnt int

	spanID int

	rc    resources
	owner *resourceScope   // set in span scopes, which define trees
	edges []*resourceScope // set in DAG scopes, it's the linearized parent set

	name    string   // for debugging purposes
	trace   *trace   // debug tracing
	metrics *metrics // metrics collection
}

var _ network.ResourceScope = (*resourceScope)(nil)
var _ network.ResourceScopeSpan = (*resourceScope)(nil)

func newResourceScope(limit Limit, edges []*resourceScope, name string, trace *trace, metrics *metrics) *resourceScope {
	for _, e := range edges {
		e.IncRef()
	}
	r := &resourceScope{
		rc:      resources{limit: limit},
		edges:   edges,
		name:    name,
		trace:   trace,
		metrics: metrics,
	}
	r.trace.CreateScope(name, limit)
	return r
}

func newResourceScopeSpan(owner *resourceScope, id int) *resourceScope {
	r := &resourceScope{
		rc:      resources{limit: owner.rc.limit},
		owner:   owner,
		name:    fmt.Sprintf("%s.span-%d", owner.name, id),
		trace:   owner.trace,
		metrics: owner.metrics,
	}
	r.trace.CreateScope(r.name, r.rc.limit)
	return r
}

// IsSpan will return true if this name was created by newResourceScopeSpan
func IsSpan(name string) bool {
	return strings.Contains(name, ".span-")
}

// Resources implementation
func (rc *resources) checkMemory(rsvp int64, prio uint8) error {
	// overflow check; this also has the side effect that we cannot reserve negative memory.
	newmem := rc.memory + rsvp
	limit := rc.limit.GetMemoryLimit()
	threshold := (1 + int64(prio)) * limit / 256

	if newmem > threshold {
		return &errMemoryLimitExceeded{
			current:   rc.memory,
			attempted: rsvp,
			limit:     limit,
			priority:  prio,
			err:       network.ErrResourceLimitExceeded,
		}
	}
	return nil
}

func (rc *resources) reserveMemory(size int64, prio uint8) error {
	if err := rc.checkMemory(size, prio); err != nil {
		return err
	}

	rc.memory += size
	return nil
}

func (rc *resources) releaseMemory(size int64) {
	rc.memory -= size

	// sanity check for bugs upstream
	if rc.memory < 0 {
		log.Warn("BUG: too much memory released")
		rc.memory = 0
	}
}

func (rc *resources) addStream(dir network.Direction) error {
	if dir == network.DirInbound {
		return rc.addStreams(1, 0)
	}
	return rc.addStreams(0, 1)
}

func (rc *resources) addStreams(incount, outcount int) error {
	if incount > 0 {
		limit := rc.limit.GetStreamLimit(network.DirInbound)
		if rc.nstreamsIn+incount > limit {
			return &errStreamOrConnLimitExceeded{
				current:   rc.nstreamsIn,
				attempted: incount,
				limit:     limit,
				err:       fmt.Errorf("cannot reserve inbound stream: %w", network.ErrResourceLimitExceeded),
			}
		}
	}
	if outcount > 0 {
		limit := rc.limit.GetStreamLimit(network.DirOutbound)
		if rc.nstreamsOut+outcount > limit {
			return &errStreamOrConnLimitExceeded{
				current:   rc.nstreamsOut,
				attempted: outcount,
				limit:     limit,
				err:       fmt.Errorf("cannot reserve outbound stream: %w", network.ErrResourceLimitExceeded),
			}
		}
	}

	if limit := rc.limit.GetStreamTotalLimit(); rc.nstreamsIn+incount+rc.nstreamsOut+outcount > limit {
		return &errStreamOrConnLimitExceeded{
			current:   rc.nstreamsIn + rc.nstreamsOut,
			attempted: incount + outcount,
			limit:     limit,
			err:       fmt.Errorf("cannot reserve stream: %w", network.ErrResourceLimitExceeded),
		}
	}

	rc.nstreamsIn += incount
	rc.nstreamsOut += outcount
	return nil
}

func (rc *resources) removeStream(dir network.Direction) {
	if dir == network.DirInbound {
		rc.removeStreams(1, 0)
	} else {
		rc.removeStreams(0, 1)
	}
}

func (rc *resources) removeStreams(incount, outcount int) {
	rc.nstreamsIn -= incount
	rc.nstreamsOut -= outcount

	if rc.nstreamsIn < 0 {
		log.Warn("BUG: too many inbound streams released")
		rc.nstreamsIn = 0
	}
	if rc.nstreamsOut < 0 {
		log.Warn("BUG: too many outbound streams released")
		rc.nstreamsOut = 0
	}
}

func (rc *resources) addConn(dir network.Direction, usefd bool) error {
	var fd int
	if usefd {
		fd = 1
	}

	if dir == network.DirInbound {
		return rc.addConns(1, 0, fd)
	}

	return rc.addConns(0, 1, fd)
}

func (rc *resources) addConns(incount, outcount, fdcount int) error {
	if incount > 0 {
		limit := rc.limit.GetConnLimit(network.DirInbound)
		if rc.nconnsIn+incount > limit {
			return &errStreamOrConnLimitExceeded{
				current:   rc.nconnsIn,
				attempted: incount,
				limit:     limit,
				err:       fmt.Errorf("cannot reserve inbound connection: %w", network.ErrResourceLimitExceeded),
			}
		}
	}
	if outcount > 0 {
		limit := rc.limit.GetConnLimit(network.DirOutbound)
		if rc.nconnsOut+outcount > limit {
			return &errStreamOrConnLimitExceeded{
				current:   rc.nconnsOut,
				attempted: outcount,
				limit:     limit,
				err:       fmt.Errorf("cannot reserve outbound connection: %w", network.ErrResourceLimitExceeded),
			}
		}
	}

	if connLimit := rc.limit.GetConnTotalLimit(); rc.nconnsIn+incount+rc.nconnsOut+outcount > connLimit {
		return &errStreamOrConnLimitExceeded{
			current:   rc.nconnsIn + rc.nconnsOut,
			attempted: incount + outcount,
			limit:     connLimit,
			err:       fmt.Errorf("cannot reserve connection: %w", network.ErrResourceLimitExceeded),
		}
	}
	if fdcount > 0 {
		limit := rc.limit.GetFDLimit()
		if rc.nfd+fdcount > limit {
			return &errStreamOrConnLimitExceeded{
				current:   rc.nfd,
				attempted: fdcount,
				limit:     limit,
				err:       fmt.Errorf("cannot reserve file descriptor: %w", network.ErrResourceLimitExceeded),
			}
		}
	}

	rc.nconnsIn += incount
	rc.nconnsOut += outcount
	rc.nfd += fdcount
	return nil
}

func (rc *resources) removeConn(dir network.Direction, usefd bool) {
	var fd int
	if usefd {
		fd = 1
	}

	if dir == network.DirInbound {
		rc.removeConns(1, 0, fd)
	} else {
		rc.removeConns(0, 1, fd)
	}
}

func (rc *resources) removeConns(incount, outcount, fdcount int) {
	rc.nconnsIn -= incount
	rc.nconnsOut -= outcount
	rc.nfd -= fdcount

	if rc.nconnsIn < 0 {
		log.Warn("BUG: too many inbound connections released")
		rc.nconnsIn = 0
	}
	if rc.nconnsOut < 0 {
		log.Warn("BUG: too many outbound connections released")
		rc.nconnsOut = 0
	}
	if rc.nfd < 0 {
		log.Warn("BUG: too many file descriptors released")
		rc.nfd = 0
	}
}

func (rc *resources) stat() network.ScopeStat {
	return network.ScopeStat{
		Memory:             rc.memory,
		NumStreamsInbound:  rc.nstreamsIn,
		NumStreamsOutbound: rc.nstreamsOut,
		NumConnsInbound:    rc.nconnsIn,
		NumConnsOutbound:   rc.nconnsOut,
		NumFD:              rc.nfd,
	}
}

// resourceScope implementation
func (s *resourceScope) wrapError(err error) error {
	return fmt.Errorf("%s: %w", s.name, err)
}

func (s *resourceScope) ReserveMemory(size int, prio uint8) error {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return s.wrapError(network.ErrResourceScopeClosed)
	}

	if err := s.rc.reserveMemory(int64(size), prio); err != nil {
		log.Debugw("blocked memory reservation", logValuesMemoryLimit(s.name, "", s.rc.stat(), err)...)
		s.trace.BlockReserveMemory(s.name, prio, int64(size), s.rc.memory)
		s.metrics.BlockMemory(size)
		return s.wrapError(err)
	}

	if err := s.reserveMemoryForEdges(size, prio); err != nil {
		s.rc.releaseMemory(int64(size))
		s.metrics.BlockMemory(size)
		return s.wrapError(err)
	}

	s.trace.ReserveMemory(s.name, prio, int64(size), s.rc.memory)
	s.metrics.AllowMemory(size)
	return nil
}

func (s *resourceScope) reserveMemoryForEdges(size int, prio uint8) error {
	if s.owner != nil {
		return s.owner.ReserveMemory(size, prio)
	}

	var reserved int
	var err error
	for _, e := range s.edges {
		var stat network.ScopeStat
		stat, err = e.ReserveMemoryForChild(int64(size), prio)
		if err != nil {
			log.Debugw("blocked memory reservation from constraining edge", logValuesMemoryLimit(s.name, e.name, stat, err)...)
			break
		}

		reserved++
	}

	if err != nil {
		// we failed because of a constraint; undo memory reservations
		for _, e := range s.edges[:reserved] {
			e.ReleaseMemoryForChild(int64(size))
		}
	}

	return err
}

func (s *resourceScope) releaseMemoryForEdges(size int) {
	if s.owner != nil {
		s.owner.ReleaseMemory(size)
		return
	}

	for _, e := range s.edges {
		e.ReleaseMemoryForChild(int64(size))
	}
}

func (s *resourceScope) ReserveMemoryForChild(size int64, prio uint8) (network.ScopeStat, error) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return s.rc.stat(), s.wrapError(network.ErrResourceScopeClosed)
	}

	if err := s.rc.reserveMemory(size, prio); err != nil {
		s.trace.BlockReserveMemory(s.name, prio, size, s.rc.memory)
		return s.rc.stat(), s.wrapError(err)
	}

	s.trace.ReserveMemory(s.name, prio, size, s.rc.memory)
	return network.ScopeStat{}, nil
}

func (s *resourceScope) ReleaseMemory(size int) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.releaseMemory(int64(size))
	s.releaseMemoryForEdges(size)
	s.trace.ReleaseMemory(s.name, int64(size), s.rc.memory)
}

func (s *resourceScope) ReleaseMemoryForChild(size int64) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.releaseMemory(size)
	s.trace.ReleaseMemory(s.name, size, s.rc.memory)
}

func (s *resourceScope) AddStream(dir network.Direction) error {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return s.wrapError(network.ErrResourceScopeClosed)
	}

	if err := s.rc.addStream(dir); err != nil {
		log.Debugw("blocked stream", logValuesStreamLimit(s.name, "", dir, s.rc.stat(), err)...)
		s.trace.BlockAddStream(s.name, dir, s.rc.nstreamsIn, s.rc.nstreamsOut)
		return s.wrapError(err)
	}

	if err := s.addStreamForEdges(dir); err != nil {
		s.rc.removeStream(dir)
		return s.wrapError(err)
	}

	s.trace.AddStream(s.name, dir, s.rc.nstreamsIn, s.rc.nstreamsOut)
	return nil
}

func (s *resourceScope) addStreamForEdges(dir network.Direction) error {
	if s.owner != nil {
		return s.owner.AddStream(dir)
	}

	var err error
	var reserved int
	for _, e := range s.edges {
		var stat network.ScopeStat
		stat, err = e.AddStreamForChild(dir)
		if err != nil {
			log.Debugw("blocked stream from constraining edge", logValuesStreamLimit(s.name, e.name, dir, stat, err)...)
			break
		}
		reserved++
	}

	if err != nil {
		for _, e := range s.edges[:reserved] {
			e.RemoveStreamForChild(dir)
		}
	}

	return err
}

func (s *resourceScope) AddStreamForChild(dir network.Direction) (network.ScopeStat, error) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return s.rc.stat(), s.wrapError(network.ErrResourceScopeClosed)
	}

	if err := s.rc.addStream(dir); err != nil {
		s.trace.BlockAddStream(s.name, dir, s.rc.nstreamsIn, s.rc.nstreamsOut)
		return s.rc.stat(), s.wrapError(err)
	}

	s.trace.AddStream(s.name, dir, s.rc.nstreamsIn, s.rc.nstreamsOut)
	return network.ScopeStat{}, nil
}

func (s *resourceScope) RemoveStream(dir network.Direction) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.removeStream(dir)
	s.removeStreamForEdges(dir)
	s.trace.RemoveStream(s.name, dir, s.rc.nstreamsIn, s.rc.nstreamsOut)
}

func (s *resourceScope) removeStreamForEdges(dir network.Direction) {
	if s.owner != nil {
		s.owner.RemoveStream(dir)
		return
	}

	for _, e := range s.edges {
		e.RemoveStreamForChild(dir)
	}
}

func (s *resourceScope) RemoveStreamForChild(dir network.Direction) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.removeStream(dir)
	s.trace.RemoveStream(s.name, dir, s.rc.nstreamsIn, s.rc.nstreamsOut)
}

func (s *resourceScope) AddConn(dir network.Direction, usefd bool) error {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return s.wrapError(network.ErrResourceScopeClosed)
	}

	if err := s.rc.addConn(dir, usefd); err != nil {
		log.Debugw("blocked connection", logValuesConnLimit(s.name, "", dir, usefd, s.rc.stat(), err)...)
		s.trace.BlockAddConn(s.name, dir, usefd, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
		return s.wrapError(err)
	}

	if err := s.addConnForEdges(dir, usefd); err != nil {
		s.rc.removeConn(dir, usefd)
		return s.wrapError(err)
	}

	s.trace.AddConn(s.name, dir, usefd, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
	return nil
}

func (s *resourceScope) addConnForEdges(dir network.Direction, usefd bool) error {
	if s.owner != nil {
		return s.owner.AddConn(dir, usefd)
	}

	var err error
	var reserved int
	for _, e := range s.edges {
		var stat network.ScopeStat
		stat, err = e.AddConnForChild(dir, usefd)
		if err != nil {
			log.Debugw("blocked connection from constraining edge", logValuesConnLimit(s.name, e.name, dir, usefd, stat, err)...)
			break
		}
		reserved++
	}

	if err != nil {
		for _, e := range s.edges[:reserved] {
			e.RemoveConnForChild(dir, usefd)
		}
	}

	return err
}

func (s *resourceScope) AddConnForChild(dir network.Direction, usefd bool) (network.ScopeStat, error) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return s.rc.stat(), s.wrapError(network.ErrResourceScopeClosed)
	}

	if err := s.rc.addConn(dir, usefd); err != nil {
		s.trace.BlockAddConn(s.name, dir, usefd, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
		return s.rc.stat(), s.wrapError(err)
	}

	s.trace.AddConn(s.name, dir, usefd, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
	return network.ScopeStat{}, nil
}

func (s *resourceScope) RemoveConn(dir network.Direction, usefd bool) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.removeConn(dir, usefd)
	s.removeConnForEdges(dir, usefd)
	s.trace.RemoveConn(s.name, dir, usefd, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
}

func (s *resourceScope) removeConnForEdges(dir network.Direction, usefd bool) {
	if s.owner != nil {
		s.owner.RemoveConn(dir, usefd)
	}

	for _, e := range s.edges {
		e.RemoveConnForChild(dir, usefd)
	}
}

func (s *resourceScope) RemoveConnForChild(dir network.Direction, usefd bool) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.removeConn(dir, usefd)
	s.trace.RemoveConn(s.name, dir, usefd, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
}

func (s *resourceScope) ReserveForChild(st network.ScopeStat) error {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return s.wrapError(network.ErrResourceScopeClosed)
	}

	if err := s.rc.reserveMemory(st.Memory, network.ReservationPriorityAlways); err != nil {
		s.trace.BlockReserveMemory(s.name, 255, st.Memory, s.rc.memory)
		return s.wrapError(err)
	}

	if err := s.rc.addStreams(st.NumStreamsInbound, st.NumStreamsOutbound); err != nil {
		s.trace.BlockAddStreams(s.name, st.NumStreamsInbound, st.NumStreamsOutbound, s.rc.nstreamsIn, s.rc.nstreamsOut)
		s.rc.releaseMemory(st.Memory)
		return s.wrapError(err)
	}

	if err := s.rc.addConns(st.NumConnsInbound, st.NumConnsOutbound, st.NumFD); err != nil {
		s.trace.BlockAddConns(s.name, st.NumConnsInbound, st.NumConnsOutbound, st.NumFD, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)

		s.rc.releaseMemory(st.Memory)
		s.rc.removeStreams(st.NumStreamsInbound, st.NumStreamsOutbound)
		return s.wrapError(err)
	}

	s.trace.ReserveMemory(s.name, 255, st.Memory, s.rc.memory)
	s.trace.AddStreams(s.name, st.NumStreamsInbound, st.NumStreamsOutbound, s.rc.nstreamsIn, s.rc.nstreamsOut)
	s.trace.AddConns(s.name, st.NumConnsInbound, st.NumConnsOutbound, st.NumFD, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)

	return nil
}

func (s *resourceScope) ReleaseForChild(st network.ScopeStat) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.releaseMemory(st.Memory)
	s.rc.removeStreams(st.NumStreamsInbound, st.NumStreamsOutbound)
	s.rc.removeConns(st.NumConnsInbound, st.NumConnsOutbound, st.NumFD)

	s.trace.ReleaseMemory(s.name, st.Memory, s.rc.memory)
	s.trace.RemoveStreams(s.name, st.NumStreamsInbound, st.NumStreamsOutbound, s.rc.nstreamsIn, s.rc.nstreamsOut)
	s.trace.RemoveConns(s.name, st.NumConnsInbound, st.NumConnsOutbound, st.NumFD, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
}

func (s *resourceScope) ReleaseResources(st network.ScopeStat) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	s.rc.releaseMemory(st.Memory)
	s.rc.removeStreams(st.NumStreamsInbound, st.NumStreamsOutbound)
	s.rc.removeConns(st.NumConnsInbound, st.NumConnsOutbound, st.NumFD)

	if s.owner != nil {
		s.owner.ReleaseResources(st)
	} else {
		for _, e := range s.edges {
			e.ReleaseForChild(st)
		}
	}

	s.trace.ReleaseMemory(s.name, st.Memory, s.rc.memory)
	s.trace.RemoveStreams(s.name, st.NumStreamsInbound, st.NumStreamsOutbound, s.rc.nstreamsIn, s.rc.nstreamsOut)
	s.trace.RemoveConns(s.name, st.NumConnsInbound, st.NumConnsOutbound, st.NumFD, s.rc.nconnsIn, s.rc.nconnsOut, s.rc.nfd)
}

func (s *resourceScope) nextSpanID() int {
	s.spanID++
	return s.spanID
}

func (s *resourceScope) BeginSpan() (network.ResourceScopeSpan, error) {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return nil, s.wrapError(network.ErrResourceScopeClosed)
	}

	s.refCnt++
	return newResourceScopeSpan(s, s.nextSpanID()), nil
}

func (s *resourceScope) Done() {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return
	}

	stat := s.rc.stat()
	if s.owner != nil {
		s.owner.ReleaseResources(stat)
		s.owner.DecRef()
	} else {
		for _, e := range s.edges {
			e.ReleaseForChild(stat)
			e.DecRef()
		}
	}

	s.rc.nstreamsIn = 0
	s.rc.nstreamsOut = 0
	s.rc.nconnsIn = 0
	s.rc.nconnsOut = 0
	s.rc.nfd = 0
	s.rc.memory = 0

	s.done = true

	s.trace.DestroyScope(s.name)
}

func (s *resourceScope) Stat() network.ScopeStat {
	s.Lock()
	defer s.Unlock()

	return s.rc.stat()
}

func (s *resourceScope) IncRef() {
	s.Lock()
	defer s.Unlock()

	s.refCnt++
}

func (s *resourceScope) DecRef() {
	s.Lock()
	defer s.Unlock()

	s.refCnt--
}

func (s *resourceScope) IsUnused() bool {
	s.Lock()
	defer s.Unlock()

	if s.done {
		return true
	}

	if s.refCnt > 0 {
		return false
	}

	st := s.rc.stat()
	return st.NumStreamsInbound == 0 &&
		st.NumStreamsOutbound == 0 &&
		st.NumConnsInbound == 0 &&
		st.NumConnsOutbound == 0 &&
		st.NumFD == 0
}
