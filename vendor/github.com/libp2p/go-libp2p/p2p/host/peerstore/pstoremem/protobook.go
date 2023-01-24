package pstoremem

import (
	"errors"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	pstore "github.com/libp2p/go-libp2p/core/peerstore"
)

type protoSegment struct {
	sync.RWMutex
	protocols map[peer.ID]map[string]struct{}
}

type protoSegments [256]*protoSegment

func (s *protoSegments) get(p peer.ID) *protoSegment {
	return s[byte(p[len(p)-1])]
}

var errTooManyProtocols = errors.New("too many protocols")

type memoryProtoBook struct {
	segments protoSegments

	maxProtos int

	lk       sync.RWMutex
	interned map[string]string
}

var _ pstore.ProtoBook = (*memoryProtoBook)(nil)

type ProtoBookOption func(book *memoryProtoBook) error

func WithMaxProtocols(num int) ProtoBookOption {
	return func(pb *memoryProtoBook) error {
		pb.maxProtos = num
		return nil
	}
}

func NewProtoBook(opts ...ProtoBookOption) (*memoryProtoBook, error) {
	pb := &memoryProtoBook{
		interned: make(map[string]string, 256),
		segments: func() (ret protoSegments) {
			for i := range ret {
				ret[i] = &protoSegment{
					protocols: make(map[peer.ID]map[string]struct{}),
				}
			}
			return ret
		}(),
		maxProtos: 1024,
	}

	for _, opt := range opts {
		if err := opt(pb); err != nil {
			return nil, err
		}
	}
	return pb, nil
}

func (pb *memoryProtoBook) internProtocol(proto string) string {
	// check if it is interned with the read lock
	pb.lk.RLock()
	interned, ok := pb.interned[proto]
	pb.lk.RUnlock()

	if ok {
		return interned
	}

	// intern with the write lock
	pb.lk.Lock()
	defer pb.lk.Unlock()

	// check again in case it got interned in between locks
	interned, ok = pb.interned[proto]
	if ok {
		return interned
	}

	pb.interned[proto] = proto
	return proto
}

func (pb *memoryProtoBook) SetProtocols(p peer.ID, protos ...string) error {
	if len(protos) > pb.maxProtos {
		return errTooManyProtocols
	}

	newprotos := make(map[string]struct{}, len(protos))
	for _, proto := range protos {
		newprotos[pb.internProtocol(proto)] = struct{}{}
	}

	s := pb.segments.get(p)
	s.Lock()
	s.protocols[p] = newprotos
	s.Unlock()

	return nil
}

func (pb *memoryProtoBook) AddProtocols(p peer.ID, protos ...string) error {
	s := pb.segments.get(p)
	s.Lock()
	defer s.Unlock()

	protomap, ok := s.protocols[p]
	if !ok {
		protomap = make(map[string]struct{})
		s.protocols[p] = protomap
	}
	if len(protomap)+len(protos) > pb.maxProtos {
		return errTooManyProtocols
	}

	for _, proto := range protos {
		protomap[pb.internProtocol(proto)] = struct{}{}
	}
	return nil
}

func (pb *memoryProtoBook) GetProtocols(p peer.ID) ([]string, error) {
	s := pb.segments.get(p)
	s.RLock()
	defer s.RUnlock()

	out := make([]string, 0, len(s.protocols[p]))
	for k := range s.protocols[p] {
		out = append(out, k)
	}

	return out, nil
}

func (pb *memoryProtoBook) RemoveProtocols(p peer.ID, protos ...string) error {
	s := pb.segments.get(p)
	s.Lock()
	defer s.Unlock()

	protomap, ok := s.protocols[p]
	if !ok {
		// nothing to remove.
		return nil
	}

	for _, proto := range protos {
		delete(protomap, pb.internProtocol(proto))
	}
	return nil
}

func (pb *memoryProtoBook) SupportsProtocols(p peer.ID, protos ...string) ([]string, error) {
	s := pb.segments.get(p)
	s.RLock()
	defer s.RUnlock()

	out := make([]string, 0, len(protos))
	for _, proto := range protos {
		if _, ok := s.protocols[p][proto]; ok {
			out = append(out, proto)
		}
	}

	return out, nil
}

func (pb *memoryProtoBook) FirstSupportedProtocol(p peer.ID, protos ...string) (string, error) {
	s := pb.segments.get(p)
	s.RLock()
	defer s.RUnlock()

	for _, proto := range protos {
		if _, ok := s.protocols[p][proto]; ok {
			return proto, nil
		}
	}
	return "", nil
}

func (pb *memoryProtoBook) RemovePeer(p peer.ID) {
	s := pb.segments.get(p)
	s.Lock()
	delete(s.protocols, p)
	s.Unlock()
}
