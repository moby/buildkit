package asnutil

import (
	"errors"
	"fmt"
	"net"

	"github.com/libp2p/go-cidranger"
)

var Store *indirectAsnStore

func init() {
	Store = newIndirectAsnStore()
}

type networkWithAsn struct {
	nn  net.IPNet
	asn string
}

func (e *networkWithAsn) Network() net.IPNet {
	return e.nn
}

type asnStore struct {
	cr cidranger.Ranger
}

// AsnForIPv6 returns the AS number for the given IPv6 address.
// If no mapping exists for the given IP, this function will
// return an empty ASN and a nil error.
func (a *asnStore) AsnForIPv6(ip net.IP) (string, error) {
	if ip.To16() == nil {
		return "", errors.New("ONLY IPv6 addresses supported for now")
	}

	ns, err := a.cr.ContainingNetworks(ip)
	if err != nil {
		return "", fmt.Errorf("failed to find matching networks for the given ip: %w", err)
	}

	if len(ns) == 0 {
		return "", nil
	}

	// longest prefix match
	n := ns[len(ns)-1].(*networkWithAsn)
	return n.asn, nil
}

func newAsnStore() (*asnStore, error) {
	cr := cidranger.NewPCTrieRanger()

	for _, v := range ipv6CidrToAsnPairList {
		_, nn, err := net.ParseCIDR(v.cidr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse CIDR %s: %w", v.cidr, err)
		}

		if err := cr.Insert(&networkWithAsn{*nn, v.asn}); err != nil {
			return nil, fmt.Errorf("failed to insert CIDR %s in Trie store: %w", v.cidr, err)
		}
	}

	return &asnStore{cr}, nil
}

type indirectAsnStore struct {
	store       *asnStore
	doneLoading chan struct{}
}

// AsnForIPv6 returns the AS number for the given IPv6 address.
// If no mapping exists for the given IP, this function will
// return an empty ASN and a nil error.
func (a *indirectAsnStore) AsnForIPv6(ip net.IP) (string, error) {
	<-a.doneLoading
	return a.store.AsnForIPv6(ip)
}

func newIndirectAsnStore() *indirectAsnStore {
	a := &indirectAsnStore{
		doneLoading: make(chan struct{}),
	}

	go func() {
		defer close(a.doneLoading)
		store, err := newAsnStore()
		if err != nil {
			panic(err)
		}
		a.store = store
	}()

	return a
}
