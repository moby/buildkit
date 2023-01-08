package ping

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	mrand "math/rand"
	"time"

	logging "github.com/ipfs/go-log/v2"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

var log = logging.Logger("ping")

const (
	PingSize    = 32
	pingTimeout = time.Second * 60

	ID = "/ipfs/ping/1.0.0"

	ServiceName = "libp2p.ping"
)

type PingService struct {
	Host host.Host
}

func NewPingService(h host.Host) *PingService {
	ps := &PingService{h}
	h.SetStreamHandler(ID, ps.PingHandler)
	return ps
}

func (p *PingService) PingHandler(s network.Stream) {
	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Debugf("error attaching stream to ping service: %s", err)
		s.Reset()
		return
	}

	if err := s.Scope().ReserveMemory(PingSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("error reserving memory for ping stream: %s", err)
		s.Reset()
		return
	}
	defer s.Scope().ReleaseMemory(PingSize)

	buf := pool.Get(PingSize)
	defer pool.Put(buf)

	errCh := make(chan error, 1)
	defer close(errCh)
	timer := time.NewTimer(pingTimeout)
	defer timer.Stop()

	go func() {
		select {
		case <-timer.C:
			log.Debug("ping timeout")
		case err, ok := <-errCh:
			if ok {
				log.Debug(err)
			} else {
				log.Error("ping loop failed without error")
			}
		}
		s.Reset()
	}()

	for {
		_, err := io.ReadFull(s, buf)
		if err != nil {
			errCh <- err
			return
		}

		_, err = s.Write(buf)
		if err != nil {
			errCh <- err
			return
		}

		timer.Reset(pingTimeout)
	}
}

// Result is a result of a ping attempt, either an RTT or an error.
type Result struct {
	RTT   time.Duration
	Error error
}

func (ps *PingService) Ping(ctx context.Context, p peer.ID) <-chan Result {
	return Ping(ctx, ps.Host, p)
}

func pingError(err error) chan Result {
	ch := make(chan Result, 1)
	ch <- Result{Error: err}
	close(ch)
	return ch
}

// Ping pings the remote peer until the context is canceled, returning a stream
// of RTTs or errors.
func Ping(ctx context.Context, h host.Host, p peer.ID) <-chan Result {
	s, err := h.NewStream(network.WithUseTransient(ctx, "ping"), p, ID)
	if err != nil {
		return pingError(err)
	}

	if err := s.Scope().SetService(ServiceName); err != nil {
		log.Debugf("error attaching stream to ping service: %s", err)
		s.Reset()
		return pingError(err)
	}

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Errorf("failed to get cryptographic random: %s", err)
		s.Reset()
		return pingError(err)
	}
	ra := mrand.New(mrand.NewSource(int64(binary.BigEndian.Uint64(b))))

	ctx, cancel := context.WithCancel(ctx)

	out := make(chan Result)
	go func() {
		defer close(out)
		defer cancel()

		for ctx.Err() == nil {
			var res Result
			res.RTT, res.Error = ping(s, ra)

			// canceled, ignore everything.
			if ctx.Err() != nil {
				return
			}

			// No error, record the RTT.
			if res.Error == nil {
				h.Peerstore().RecordLatency(p, res.RTT)
			}

			select {
			case out <- res:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		// forces the ping to abort.
		<-ctx.Done()
		s.Reset()
	}()

	return out
}

func ping(s network.Stream, randReader io.Reader) (time.Duration, error) {
	if err := s.Scope().ReserveMemory(2*PingSize, network.ReservationPriorityAlways); err != nil {
		log.Debugf("error reserving memory for ping stream: %s", err)
		s.Reset()
		return 0, err
	}
	defer s.Scope().ReleaseMemory(2 * PingSize)

	buf := pool.Get(PingSize)
	defer pool.Put(buf)

	if _, err := io.ReadFull(randReader, buf); err != nil {
		return 0, err
	}

	before := time.Now()
	if _, err := s.Write(buf); err != nil {
		return 0, err
	}

	rbuf := pool.Get(PingSize)
	defer pool.Put(rbuf)

	if _, err := io.ReadFull(s, rbuf); err != nil {
		return 0, err
	}

	if !bytes.Equal(buf, rbuf) {
		return 0, errors.New("ping packet was incorrect")
	}

	return time.Since(before), nil
}
