package pstoremem

import (
	"bytes"

	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"
)

func isFDCostlyTransport(a ma.Multiaddr) bool {
	return mafmt.TCP.Matches(a)
}

type addrList []ma.Multiaddr

func (al addrList) Len() int      { return len(al) }
func (al addrList) Swap(i, j int) { al[i], al[j] = al[j], al[i] }

func (al addrList) Less(i, j int) bool {
	a := al[i]
	b := al[j]

	// dial localhost addresses next, they should fail immediately
	lba := manet.IsIPLoopback(a)
	lbb := manet.IsIPLoopback(b)
	if lba && !lbb {
		return true
	}

	// dial utp and similar 'non-fd-consuming' addresses first
	fda := isFDCostlyTransport(a)
	fdb := isFDCostlyTransport(b)
	if !fda {
		return fdb
	}

	// if 'b' doesnt take a file descriptor
	if !fdb {
		return false
	}

	// if 'b' is loopback and both take file descriptors
	if lbb {
		return false
	}

	// for the rest, just sort by bytes
	return bytes.Compare(a.Bytes(), b.Bytes()) > 0
}
