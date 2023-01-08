package introspection

import (
	"io"

	"github.com/libp2p/go-libp2p/core/introspection/pb"
)

// Introspector is the interface to be satisfied by components that are capable
// of spelunking the state of the system, and representing in accordance with
// the introspection schema.
//
// It's very rare to build a custom implementation of this interface;
// it exists mostly for mocking. In most cases, you'll end up using the
// default introspector.
//
// Introspector implementations are usually injected in introspection endpoints
// to serve the data to clients, but they can also be used separately for
// embedding or testing.
//
// Experimental.
type Introspector interface {
	io.Closer

	// FetchRuntime returns the runtime information of the system.
	FetchRuntime() (*pb.Runtime, error)

	// FetchFullState returns the full state cross-cut of the running system.
	FetchFullState() (*pb.State, error)

	// EventChan returns the channel where all eventbus events are dumped,
	// decorated with their corresponding event metadata, ready to send over
	// the wire.
	EventChan() <-chan *pb.Event

	// EventMetadata returns the metadata of all events known to the
	// Introspector.
	EventMetadata() []*pb.EventType
}
