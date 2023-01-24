package nat

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// Mapping represents a port mapping in a NAT.
type Mapping interface {
	// NAT returns the NAT object this Mapping belongs to.
	NAT() *NAT

	// Protocol returns the protocol of this port mapping. This is either
	// "tcp" or "udp" as no other protocols are likely to be NAT-supported.
	Protocol() string

	// InternalPort returns the internal device port. Mapping will continue to
	// try to map InternalPort() to an external facing port.
	InternalPort() int

	// ExternalPort returns the external facing port. If the mapping is not
	// established, port will be 0
	ExternalPort() int

	// ExternalAddr returns the external facing address. If the mapping is not
	// established, addr will be nil, and and ErrNoMapping will be returned.
	ExternalAddr() (addr net.Addr, err error)

	// Close closes the port mapping
	Close() error
}

// keeps republishing
type mapping struct {
	sync.Mutex // guards all fields

	nat     *NAT
	proto   string
	intport int
	extport int

	cached    net.IP
	cacheTime time.Time
	cacheLk   sync.Mutex
}

func (m *mapping) NAT() *NAT {
	m.Lock()
	defer m.Unlock()
	return m.nat
}

func (m *mapping) Protocol() string {
	m.Lock()
	defer m.Unlock()
	return m.proto
}

func (m *mapping) InternalPort() int {
	m.Lock()
	defer m.Unlock()
	return m.intport
}

func (m *mapping) ExternalPort() int {
	m.Lock()
	defer m.Unlock()
	return m.extport
}

func (m *mapping) setExternalPort(p int) {
	m.Lock()
	defer m.Unlock()
	m.extport = p
}

func (m *mapping) ExternalAddr() (net.Addr, error) {
	m.cacheLk.Lock()
	defer m.cacheLk.Unlock()
	oport := m.ExternalPort()
	if oport == 0 {
		// dont even try right now.
		return nil, ErrNoMapping
	}

	if time.Since(m.cacheTime) >= CacheTime {
		m.nat.natmu.Lock()
		cval, err := m.nat.nat.GetExternalAddress()
		m.nat.natmu.Unlock()

		if err != nil {
			return nil, err
		}

		m.cached = cval
		m.cacheTime = time.Now()
	}
	switch m.Protocol() {
	case "tcp":
		return &net.TCPAddr{
			IP:   m.cached,
			Port: oport,
		}, nil
	case "udp":
		return &net.UDPAddr{
			IP:   m.cached,
			Port: oport,
		}, nil
	default:
		panic(fmt.Sprintf("invalid protocol %q", m.Protocol()))
	}
}

func (m *mapping) Close() error {
	m.nat.removeMapping(m)
	return nil
}
