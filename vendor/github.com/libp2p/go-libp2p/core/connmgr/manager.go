// Package connmgr provides connection tracking and management interfaces for libp2p.
//
// The ConnManager interface exported from this package allows libp2p to enforce an
// upper bound on the total number of open connections. To avoid service disruptions,
// connections can be tagged with metadata and optionally "protected" to ensure that
// essential connections are not arbitrarily cut.
package connmgr

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// SupportsDecay evaluates if the provided ConnManager supports decay, and if
// so, it returns the Decayer object. Refer to godocs on Decayer for more info.
func SupportsDecay(mgr ConnManager) (Decayer, bool) {
	d, ok := mgr.(Decayer)
	return d, ok
}

// ConnManager tracks connections to peers, and allows consumers to associate
// metadata with each peer.
//
// It enables connections to be trimmed based on implementation-defined
// heuristics. The ConnManager allows libp2p to enforce an upper bound on the
// total number of open connections.
//
// ConnManagers supporting decaying tags implement Decayer. Use the
// SupportsDecay function to safely cast an instance to Decayer, if supported.
type ConnManager interface {
	// TagPeer tags a peer with a string, associating a weight with the tag.
	TagPeer(peer.ID, string, int)

	// Untag removes the tagged value from the peer.
	UntagPeer(p peer.ID, tag string)

	// UpsertTag updates an existing tag or inserts a new one.
	//
	// The connection manager calls the upsert function supplying the current
	// value of the tag (or zero if inexistent). The return value is used as
	// the new value of the tag.
	UpsertTag(p peer.ID, tag string, upsert func(int) int)

	// GetTagInfo returns the metadata associated with the peer,
	// or nil if no metadata has been recorded for the peer.
	GetTagInfo(p peer.ID) *TagInfo

	// TrimOpenConns terminates open connections based on an implementation-defined
	// heuristic.
	TrimOpenConns(ctx context.Context)

	// Notifee returns an implementation that can be called back to inform of
	// opened and closed connections.
	Notifee() network.Notifiee

	// Protect protects a peer from having its connection(s) pruned.
	//
	// Tagging allows different parts of the system to manage protections without interfering with one another.
	//
	// Calls to Protect() with the same tag are idempotent. They are not refcounted, so after multiple calls
	// to Protect() with the same tag, a single Unprotect() call bearing the same tag will revoke the protection.
	Protect(id peer.ID, tag string)

	// Unprotect removes a protection that may have been placed on a peer, under the specified tag.
	//
	// The return value indicates whether the peer continues to be protected after this call, by way of a different tag.
	// See notes on Protect() for more info.
	Unprotect(id peer.ID, tag string) (protected bool)

	// IsProtected returns true if the peer is protected for some tag; if the tag is the empty string
	// then it will return true if the peer is protected for any tag
	IsProtected(id peer.ID, tag string) (protected bool)

	// Close closes the connection manager and stops background processes.
	Close() error
}

// TagInfo stores metadata associated with a peer.
type TagInfo struct {
	FirstSeen time.Time
	Value     int

	// Tags maps tag ids to the numerical values.
	Tags map[string]int

	// Conns maps connection ids (such as remote multiaddr) to their creation time.
	Conns map[string]time.Time
}
