package connmgr

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/benbjohnson/clock"
)

// DefaultResolution is the default resolution of the decay tracker.
var DefaultResolution = 1 * time.Minute

// bumpCmd represents a bump command.
type bumpCmd struct {
	peer  peer.ID
	tag   *decayingTag
	delta int
}

// removeCmd represents a tag removal command.
type removeCmd struct {
	peer peer.ID
	tag  *decayingTag
}

// decayer tracks and manages all decaying tags and their values.
type decayer struct {
	cfg   *DecayerCfg
	mgr   *BasicConnMgr
	clock clock.Clock // for testing.

	tagsMu    sync.Mutex
	knownTags map[string]*decayingTag

	// lastTick stores the last time the decayer ticked. Guarded by atomic.
	lastTick atomic.Value

	// bumpTagCh queues bump commands to be processed by the loop.
	bumpTagCh   chan bumpCmd
	removeTagCh chan removeCmd
	closeTagCh  chan *decayingTag

	// closure thingies.
	closeCh chan struct{}
	doneCh  chan struct{}
	err     error
}

var _ connmgr.Decayer = (*decayer)(nil)

// DecayerCfg is the configuration object for the Decayer.
type DecayerCfg struct {
	Resolution time.Duration
	Clock      clock.Clock
}

// WithDefaults writes the default values on this DecayerConfig instance,
// and returns itself for chainability.
//
//	cfg := (&DecayerCfg{}).WithDefaults()
//	cfg.Resolution = 30 * time.Second
//	t := NewDecayer(cfg, cm)
func (cfg *DecayerCfg) WithDefaults() *DecayerCfg {
	cfg.Resolution = DefaultResolution
	return cfg
}

// NewDecayer creates a new decaying tag registry.
func NewDecayer(cfg *DecayerCfg, mgr *BasicConnMgr) (*decayer, error) {
	// use real time if the Clock in the config is nil.
	if cfg.Clock == nil {
		cfg.Clock = clock.New()
	}

	d := &decayer{
		cfg:         cfg,
		mgr:         mgr,
		clock:       cfg.Clock,
		knownTags:   make(map[string]*decayingTag),
		bumpTagCh:   make(chan bumpCmd, 128),
		removeTagCh: make(chan removeCmd, 128),
		closeTagCh:  make(chan *decayingTag, 128),
		closeCh:     make(chan struct{}),
		doneCh:      make(chan struct{}),
	}

	d.lastTick.Store(d.clock.Now())

	// kick things off.
	go d.process()

	return d, nil
}

func (d *decayer) RegisterDecayingTag(name string, interval time.Duration, decayFn connmgr.DecayFn, bumpFn connmgr.BumpFn) (connmgr.DecayingTag, error) {
	d.tagsMu.Lock()
	defer d.tagsMu.Unlock()

	if _, ok := d.knownTags[name]; ok {
		return nil, fmt.Errorf("decaying tag with name %s already exists", name)
	}

	if interval < d.cfg.Resolution {
		log.Warnf("decay interval for %s (%s) was lower than tracker's resolution (%s); overridden to resolution",
			name, interval, d.cfg.Resolution)
		interval = d.cfg.Resolution
	}

	if interval%d.cfg.Resolution != 0 {
		log.Warnf("decay interval for tag %s (%s) is not a multiple of tracker's resolution (%s); "+
			"some precision may be lost", name, interval, d.cfg.Resolution)
	}

	lastTick := d.lastTick.Load().(time.Time)
	tag := &decayingTag{
		trkr:     d,
		name:     name,
		interval: interval,
		nextTick: lastTick.Add(interval),
		decayFn:  decayFn,
		bumpFn:   bumpFn,
	}

	d.knownTags[name] = tag
	return tag, nil
}

// Close closes the Decayer. It is idempotent.
func (d *decayer) Close() error {
	select {
	case <-d.doneCh:
		return d.err
	default:
	}

	close(d.closeCh)
	<-d.doneCh
	return d.err
}

// process is the heart of the tracker. It performs the following duties:
//
//  1. Manages decay.
//  2. Applies score bumps.
//  3. Yields when closed.
func (d *decayer) process() {
	defer close(d.doneCh)

	ticker := d.clock.Ticker(d.cfg.Resolution)
	defer ticker.Stop()

	var (
		bmp   bumpCmd
		now   time.Time
		visit = make(map[*decayingTag]struct{})
	)

	for {
		select {
		case now = <-ticker.C:
			d.lastTick.Store(now)

			d.tagsMu.Lock()
			for _, tag := range d.knownTags {
				if tag.nextTick.After(now) {
					// skip the tag.
					continue
				}
				// Mark the tag to be updated in this round.
				visit[tag] = struct{}{}
			}
			d.tagsMu.Unlock()

			// Visit each peer, and decay tags that need to be decayed.
			for _, s := range d.mgr.segments {
				s.Lock()

				// Entered a segment that contains peers. Process each peer.
				for _, p := range s.peers {
					for tag, v := range p.decaying {
						if _, ok := visit[tag]; !ok {
							// skip this tag.
							continue
						}

						// ~ this value needs to be visited. ~
						var delta int
						if after, rm := tag.decayFn(*v); rm {
							// delete the value and move on to the next tag.
							delta -= v.Value
							delete(p.decaying, tag)
						} else {
							// accumulate the delta, and apply the changes.
							delta += after - v.Value
							v.Value, v.LastVisit = after, now
						}
						p.value += delta
					}
				}

				s.Unlock()
			}

			// Reset each tag's next visit round, and clear the visited set.
			for tag := range visit {
				tag.nextTick = tag.nextTick.Add(tag.interval)
				delete(visit, tag)
			}

		case bmp = <-d.bumpTagCh:
			var (
				now       = d.clock.Now()
				peer, tag = bmp.peer, bmp.tag
			)

			s := d.mgr.segments.get(peer)
			s.Lock()

			p := s.tagInfoFor(peer)
			v, ok := p.decaying[tag]
			if !ok {
				v = &connmgr.DecayingValue{
					Tag:       tag,
					Peer:      peer,
					LastVisit: now,
					Added:     now,
					Value:     0,
				}
				p.decaying[tag] = v
			}

			prev := v.Value
			v.Value, v.LastVisit = v.Tag.(*decayingTag).bumpFn(*v, bmp.delta), now
			p.value += v.Value - prev

			s.Unlock()

		case rm := <-d.removeTagCh:
			s := d.mgr.segments.get(rm.peer)
			s.Lock()

			p := s.tagInfoFor(rm.peer)
			v, ok := p.decaying[rm.tag]
			if !ok {
				s.Unlock()
				continue
			}
			p.value -= v.Value
			delete(p.decaying, rm.tag)
			s.Unlock()

		case t := <-d.closeTagCh:
			// Stop tracking the tag.
			d.tagsMu.Lock()
			delete(d.knownTags, t.name)
			d.tagsMu.Unlock()

			// Remove the tag from all peers that had it in the connmgr.
			for _, s := range d.mgr.segments {
				// visit all segments, and attempt to remove the tag from all the peers it stores.
				s.Lock()
				for _, p := range s.peers {
					if dt, ok := p.decaying[t]; ok {
						// decrease the value of the tagInfo, and delete the tag.
						p.value -= dt.Value
						delete(p.decaying, t)
					}
				}
				s.Unlock()
			}

		case <-d.closeCh:
			return
		}
	}
}

// decayingTag represents a decaying tag, with an associated decay interval, a
// decay function, and a bump function.
type decayingTag struct {
	trkr     *decayer
	name     string
	interval time.Duration
	nextTick time.Time
	decayFn  connmgr.DecayFn
	bumpFn   connmgr.BumpFn

	// closed marks this tag as closed, so that if it's bumped after being
	// closed, we can return an error. 0 = false; 1 = true; guarded by atomic.
	closed int32
}

var _ connmgr.DecayingTag = (*decayingTag)(nil)

func (t *decayingTag) Name() string {
	return t.name
}

func (t *decayingTag) Interval() time.Duration {
	return t.interval
}

// Bump bumps a tag for this peer.
func (t *decayingTag) Bump(p peer.ID, delta int) error {
	if atomic.LoadInt32(&t.closed) == 1 {
		return fmt.Errorf("decaying tag %s had been closed; no further bumps are accepted", t.name)
	}

	bmp := bumpCmd{peer: p, tag: t, delta: delta}

	select {
	case t.trkr.bumpTagCh <- bmp:
		return nil
	default:
		return fmt.Errorf(
			"unable to bump decaying tag for peer %s, tag %s, delta %d; queue full (len=%d)",
			p.Pretty(), t.name, delta, len(t.trkr.bumpTagCh))
	}
}

func (t *decayingTag) Remove(p peer.ID) error {
	if atomic.LoadInt32(&t.closed) == 1 {
		return fmt.Errorf("decaying tag %s had been closed; no further removals are accepted", t.name)
	}

	rm := removeCmd{peer: p, tag: t}

	select {
	case t.trkr.removeTagCh <- rm:
		return nil
	default:
		return fmt.Errorf(
			"unable to remove decaying tag for peer %s, tag %s; queue full (len=%d)",
			p.Pretty(), t.name, len(t.trkr.removeTagCh))
	}
}

func (t *decayingTag) Close() error {
	if !atomic.CompareAndSwapInt32(&t.closed, 0, 1) {
		log.Warnf("duplicate decaying tag closure: %s; skipping", t.name)
		return nil
	}

	select {
	case t.trkr.closeTagCh <- t:
		return nil
	default:
		return fmt.Errorf("unable to close decaying tag %s; queue full (len=%d)", t.name, len(t.trkr.closeTagCh))
	}
}
