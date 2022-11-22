package llbsolver

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"os"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/leases"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/leaseutil"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
)

const (
	recordsBucket = "_records"
)

type HistoryQueueOpt struct {
	DB           *bolt.DB
	LeaseManager leases.Manager
	ContentStore content.Store
}

type HistoryQueue struct {
	mu       sync.Mutex
	initOnce sync.Once
	HistoryQueueOpt
	ps     *pubsub[*controlapi.BuildHistoryEvent]
	active map[string]*controlapi.BuildHistoryRecord
}

func NewHistoryQueue(opt HistoryQueueOpt) *HistoryQueue {
	return &HistoryQueue{
		HistoryQueueOpt: opt,
		ps: &pubsub[*controlapi.BuildHistoryEvent]{
			m: map[*channel[*controlapi.BuildHistoryEvent]]struct{}{},
		},
		active: map[string]*controlapi.BuildHistoryRecord{},
	}
}

func (h *HistoryQueue) init() error {
	var err error
	h.initOnce.Do(func() {
		err = h.DB.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists([]byte(recordsBucket))
			return err
		})
	})
	return err
}

func (h *HistoryQueue) leaseID(id string) string {
	return "ref_" + id
}

func (h *HistoryQueue) addResource(ctx context.Context, l leases.Lease, desc *controlapi.Descriptor) error {
	if desc == nil {
		return nil
	}
	return h.LeaseManager.AddResource(ctx, l, leases.Resource{
		ID:   string(desc.Digest),
		Type: "content",
	})
}

func (h *HistoryQueue) Status(ctx context.Context, ref string, st chan<- *client.SolveStatus) error {
	var br controlapi.BuildHistoryRecord
	if err := h.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(recordsBucket))
		if b == nil {
			return nil
		}
		dt := b.Get([]byte(ref))
		if dt == nil {
			return os.ErrNotExist
		}

		if err := br.Unmarshal(dt); err != nil {
			return errors.Wrapf(err, "failed to unmarshal build record %s", ref)
		}
		return nil
	}); err != nil {
		return err
	}

	if br.Logs == nil {
		return nil
	}

	ra, err := h.ContentStore.ReaderAt(ctx, ocispecs.Descriptor{
		Digest:    br.Logs.Digest,
		Size:      br.Logs.Size_,
		MediaType: br.Logs.MediaType,
	})
	if err != nil {
		return err
	}
	defer ra.Close()

	brdr := bufio.NewReader(&reader{ReaderAt: ra})

	buf := make([]byte, 32*1024)

	for {
		_, err := io.ReadAtLeast(brdr, buf[:4], 4)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		sz := binary.LittleEndian.Uint32(buf[:4])
		if sz > uint32(len(buf)) {
			buf = make([]byte, sz)
		}
		_, err = io.ReadAtLeast(brdr, buf[:sz], int(sz))
		if err != nil {
			return err
		}
		var sr controlapi.StatusResponse
		if err := sr.Unmarshal(buf[:sz]); err != nil {
			return err
		}
		st <- client.NewSolveStatus(&sr)
	}

	return nil
}

func (h *HistoryQueue) Update(ctx context.Context, e *controlapi.BuildHistoryEvent) error {
	h.init()
	h.mu.Lock()
	defer h.mu.Unlock()

	if e.Type == controlapi.BuildHistoryEventType_STARTED {
		h.active[e.Record.Ref] = e.Record
		h.ps.Send(e)
	}

	if e.Type == controlapi.BuildHistoryEventType_COMPLETE {
		delete(h.active, e.Record.Ref)
		if err := h.DB.Update(func(tx *bolt.Tx) (err error) {
			b := tx.Bucket([]byte(recordsBucket))
			if b == nil {
				return nil
			}
			dt, err := e.Record.Marshal()
			if err != nil {
				return err
			}

			l, err := h.LeaseManager.Create(ctx, leases.WithID(h.leaseID(e.Record.Ref)))
			if err != nil {
				return err
			}

			defer func() {
				if err != nil {
					h.LeaseManager.Delete(ctx, l)
				}
			}()

			if err := h.addResource(ctx, l, e.Record.Logs); err != nil {
				return err
			}

			return b.Put([]byte(e.Record.Ref), dt)
		}); err != nil {
			return err
		}
		h.ps.Send(e)
	}
	return nil
}

func (h *HistoryQueue) ImportStatus(ctx context.Context, ch chan *client.SolveStatus) (_ *ocispecs.Descriptor, _ func(), err error) {
	defer func() {
		if ch == nil {
			return
		}
		for range ch {
		}
	}()

	l, err := h.LeaseManager.Create(ctx, leases.WithRandomID(), leases.WithExpiration(5*time.Minute), leaseutil.MakeTemporary)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			h.LeaseManager.Delete(ctx, l)
		}
	}()
	ctx = leases.WithLease(ctx, l.ID)

	w, err := content.OpenWriter(ctx, h.ContentStore, content.WithRef("status-"+h.leaseID(l.ID)))
	if err != nil {
		return nil, nil, err
	}
	bufW := bufio.NewWriter(w)

	defer func() {
		if err != nil && w != nil {
			w.Close()
		}
	}()

	dgst := digest.Canonical.Digester()
	total := 0

	buf := make([]byte, 32*1024)
	for st := range ch {
		hdr := make([]byte, 4)
		for _, pst := range st.Marshal() {
			sz := pst.Size()
			if len(buf) < sz {
				buf = make([]byte, sz)
			}
			n, err := pst.MarshalTo(buf)
			if err != nil {
				return nil, nil, err
			}
			binary.LittleEndian.PutUint32(hdr, uint32(n))
			if _, err := bufW.Write(hdr); err != nil {
				return nil, nil, err
			}
			if _, err := bufW.Write(buf[:n]); err != nil {
				return nil, nil, err
			}
			dgst.Hash().Write(hdr)
			dgst.Hash().Write(buf[:n])
			total += 4 + n
		}
	}
	if err := bufW.Flush(); err != nil {
		return nil, nil, err
	}

	if err := w.Commit(ctx, int64(total), dgst.Digest()); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return nil, nil, err
		}
	}
	w = nil

	return &ocispecs.Descriptor{
			MediaType: "application/vnd.buildkit.status.v0",
			Digest:    dgst.Digest(),
			Size:      int64(total),
		},
		func() {
			h.LeaseManager.Delete(context.TODO(), l)
		}, nil
}

func (h *HistoryQueue) Listen(ctx context.Context, ref string, active bool, f func(*controlapi.BuildHistoryEvent) error) error {
	h.init()

	h.mu.Lock()
	sub := h.ps.Subscribe()
	defer sub.close()

	for _, e := range h.active {
		sub.ps.Send(&controlapi.BuildHistoryEvent{
			Type:   controlapi.BuildHistoryEventType_STARTED,
			Record: e,
		})
	}
	h.mu.Unlock()

	if !active {
		if err := h.DB.View(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte(recordsBucket))
			if b == nil {
				return nil
			}
			return b.ForEach(func(key, dt []byte) error {
				var br controlapi.BuildHistoryRecord
				if err := br.Unmarshal(dt); err != nil {
					return errors.Wrapf(err, "failed to unmarshal build record %s", key)
				}
				if err := f(&controlapi.BuildHistoryEvent{
					Record: &br,
					Type:   controlapi.BuildHistoryEventType_COMPLETE,
				}); err != nil {
					return err
				}
				return nil
			})
		}); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-sub.ch:
			if err := f(e); err != nil {
				return err
			}
		case <-sub.done:
			return nil
		}
	}
}

type pubsub[T any] struct {
	mu sync.Mutex
	m  map[*channel[T]]struct{}
}

func (p *pubsub[T]) Subscribe() *channel[T] {
	p.mu.Lock()
	c := &channel[T]{
		ps:   p,
		ch:   make(chan T, 32),
		done: make(chan struct{}),
	}
	p.m[c] = struct{}{}
	p.mu.Unlock()
	return c
}

func (p *pubsub[T]) Send(v T) {
	p.mu.Lock()
	for c := range p.m {
		go c.send(v)
	}
	p.mu.Unlock()
}

type channel[T any] struct {
	ps        *pubsub[T]
	ch        chan T
	done      chan struct{}
	closeOnce sync.Once
}

func (p *channel[T]) send(v T) {
	select {
	case p.ch <- v:
	case <-p.done:
	}
}

func (p *channel[T]) close() {
	p.closeOnce.Do(func() {
		p.ps.mu.Lock()
		delete(p.ps.m, p)
		p.ps.mu.Unlock()
		close(p.done)
	})
}

type reader struct {
	io.ReaderAt
	pos int64
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, r.pos)
	r.pos += int64(len(p))
	return n, err
}
