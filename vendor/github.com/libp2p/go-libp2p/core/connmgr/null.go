package connmgr

import (
	"context"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// NullConnMgr is a ConnMgr that provides no functionality.
type NullConnMgr struct{}

var _ ConnManager = (*NullConnMgr)(nil)

func (NullConnMgr) TagPeer(peer.ID, string, int)             {}
func (NullConnMgr) UntagPeer(peer.ID, string)                {}
func (NullConnMgr) UpsertTag(peer.ID, string, func(int) int) {}
func (NullConnMgr) GetTagInfo(peer.ID) *TagInfo              { return &TagInfo{} }
func (NullConnMgr) TrimOpenConns(ctx context.Context)        {}
func (NullConnMgr) Notifee() network.Notifiee                { return network.GlobalNoopNotifiee }
func (NullConnMgr) Protect(peer.ID, string)                  {}
func (NullConnMgr) Unprotect(peer.ID, string) bool           { return false }
func (NullConnMgr) IsProtected(peer.ID, string) bool         { return false }
func (NullConnMgr) Close() error                             { return nil }
