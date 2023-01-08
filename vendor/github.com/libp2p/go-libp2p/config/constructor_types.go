package config

import (
	"fmt"
	"reflect"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/transport"
)

var (
	// interfaces
	hostType      = reflect.TypeOf((*host.Host)(nil)).Elem()
	networkType   = reflect.TypeOf((*network.Network)(nil)).Elem()
	transportType = reflect.TypeOf((*transport.Transport)(nil)).Elem()
	muxType       = reflect.TypeOf((*network.Multiplexer)(nil)).Elem()
	securityType  = reflect.TypeOf((*sec.SecureTransport)(nil)).Elem()
	privKeyType   = reflect.TypeOf((*crypto.PrivKey)(nil)).Elem()
	pubKeyType    = reflect.TypeOf((*crypto.PubKey)(nil)).Elem()
	pstoreType    = reflect.TypeOf((*peerstore.Peerstore)(nil)).Elem()
	connGaterType = reflect.TypeOf((*connmgr.ConnectionGater)(nil)).Elem()
	upgraderType  = reflect.TypeOf((*transport.Upgrader)(nil)).Elem()
	rcmgrType     = reflect.TypeOf((*network.ResourceManager)(nil)).Elem()

	// concrete types
	peerIDType = reflect.TypeOf((peer.ID)(""))
	pskType    = reflect.TypeOf((pnet.PSK)(nil))
)

var argTypes = map[reflect.Type]constructor{
	upgraderType: func(_ host.Host, u transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return u
	},
	hostType: func(h host.Host, _ transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return h
	},
	networkType: func(h host.Host, _ transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return h.Network()
	},
	pskType: func(_ host.Host, _ transport.Upgrader, psk pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return psk
	},
	connGaterType: func(_ host.Host, _ transport.Upgrader, _ pnet.PSK, cg connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return cg
	},
	peerIDType: func(h host.Host, _ transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return h.ID()
	},
	privKeyType: func(h host.Host, _ transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return h.Peerstore().PrivKey(h.ID())
	},
	pubKeyType: func(h host.Host, _ transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return h.Peerstore().PubKey(h.ID())
	},
	pstoreType: func(h host.Host, _ transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, _ network.ResourceManager) interface{} {
		return h.Peerstore()
	},
	rcmgrType: func(_ host.Host, _ transport.Upgrader, _ pnet.PSK, _ connmgr.ConnectionGater, rcmgr network.ResourceManager) interface{} {
		return rcmgr
	},
}

func newArgTypeSet(types ...reflect.Type) map[reflect.Type]constructor {
	result := make(map[reflect.Type]constructor, len(types))
	for _, ty := range types {
		c, ok := argTypes[ty]
		if !ok {
			panic(fmt.Sprintf("missing constructor for type %s", ty))
		}
		result[ty] = c
	}
	return result
}
