package swarm

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// dialWorkerFunc is used by dialSync to spawn a new dial worker
type dialWorkerFunc func(peer.ID, <-chan dialRequest)

// newDialSync constructs a new dialSync
func newDialSync(worker dialWorkerFunc) *dialSync {
	return &dialSync{
		dials:      make(map[peer.ID]*activeDial),
		dialWorker: worker,
	}
}

// dialSync is a dial synchronization helper that ensures that at most one dial
// to any given peer is active at any given time.
type dialSync struct {
	mutex      sync.Mutex
	dials      map[peer.ID]*activeDial
	dialWorker dialWorkerFunc
}

type activeDial struct {
	refCnt int

	ctx    context.Context
	cancel func()

	reqch chan dialRequest
}

func (ad *activeDial) close() {
	ad.cancel()
	close(ad.reqch)
}

func (ad *activeDial) dial(ctx context.Context) (*Conn, error) {
	dialCtx := ad.ctx

	if forceDirect, reason := network.GetForceDirectDial(ctx); forceDirect {
		dialCtx = network.WithForceDirectDial(dialCtx, reason)
	}
	if simConnect, isClient, reason := network.GetSimultaneousConnect(ctx); simConnect {
		dialCtx = network.WithSimultaneousConnect(dialCtx, isClient, reason)
	}

	resch := make(chan dialResponse, 1)
	select {
	case ad.reqch <- dialRequest{ctx: dialCtx, resch: resch}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case res := <-resch:
		return res.conn, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (ds *dialSync) getActiveDial(p peer.ID) (*activeDial, error) {
	ds.mutex.Lock()
	defer ds.mutex.Unlock()

	actd, ok := ds.dials[p]
	if !ok {
		// This code intentionally uses the background context. Otherwise, if the first call
		// to Dial is canceled, subsequent dial calls will also be canceled.
		ctx, cancel := context.WithCancel(context.Background())
		actd = &activeDial{
			ctx:    ctx,
			cancel: cancel,
			reqch:  make(chan dialRequest),
		}
		go ds.dialWorker(p, actd.reqch)
		ds.dials[p] = actd
	}
	// increase ref count before dropping mutex
	actd.refCnt++
	return actd, nil
}

// Dial initiates a dial to the given peer if there are none in progress
// then waits for the dial to that peer to complete.
func (ds *dialSync) Dial(ctx context.Context, p peer.ID) (*Conn, error) {
	ad, err := ds.getActiveDial(p)
	if err != nil {
		return nil, err
	}

	defer func() {
		ds.mutex.Lock()
		defer ds.mutex.Unlock()
		ad.refCnt--
		if ad.refCnt == 0 {
			ad.close()
			delete(ds.dials, p)
		}
	}()
	return ad.dial(ctx)
}
