package libp2pquic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	p2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"

	ma "github.com/multiformats/go-multiaddr"
	mafmt "github.com/multiformats/go-multiaddr-fmt"
	manet "github.com/multiformats/go-multiaddr/net"

	logging "github.com/ipfs/go-log/v2"
	"github.com/lucas-clemente/quic-go"
	"github.com/minio/sha256-simd"
)

var log = logging.Logger("quic-transport")

var ErrHolePunching = errors.New("hole punching attempted; no active dial")

var quicDialContext = quic.DialContext // so we can mock it in tests

var HolePunchTimeout = 5 * time.Second

var quicConfig = &quic.Config{
	MaxIncomingStreams:         256,
	MaxIncomingUniStreams:      -1,             // disable unidirectional streams
	MaxStreamReceiveWindow:     10 * (1 << 20), // 10 MB
	MaxConnectionReceiveWindow: 15 * (1 << 20), // 15 MB
	AcceptToken: func(clientAddr net.Addr, _ *quic.Token) bool {
		// TODO(#6): require source address validation when under load
		return true
	},
	KeepAlivePeriod: 15 * time.Second,
	Versions:        []quic.VersionNumber{quic.VersionDraft29, quic.Version1},
}

const statelessResetKeyInfo = "libp2p quic stateless reset key"
const errorCodeConnectionGating = 0x47415445 // GATE in ASCII

type connManager struct {
	reuseUDP4 *reuse
	reuseUDP6 *reuse
}

func newConnManager() (*connManager, error) {
	reuseUDP4 := newReuse()
	reuseUDP6 := newReuse()

	return &connManager{
		reuseUDP4: reuseUDP4,
		reuseUDP6: reuseUDP6,
	}, nil
}

func (c *connManager) getReuse(network string) (*reuse, error) {
	switch network {
	case "udp4":
		return c.reuseUDP4, nil
	case "udp6":
		return c.reuseUDP6, nil
	default:
		return nil, errors.New("invalid network: must be either udp4 or udp6")
	}
}

func (c *connManager) Listen(network string, laddr *net.UDPAddr) (*reuseConn, error) {
	reuse, err := c.getReuse(network)
	if err != nil {
		return nil, err
	}
	return reuse.Listen(network, laddr)
}

func (c *connManager) Dial(network string, raddr *net.UDPAddr) (*reuseConn, error) {
	reuse, err := c.getReuse(network)
	if err != nil {
		return nil, err
	}
	return reuse.Dial(network, raddr)
}

func (c *connManager) Close() error {
	if err := c.reuseUDP6.Close(); err != nil {
		return err
	}
	return c.reuseUDP4.Close()
}

// The Transport implements the tpt.Transport interface for QUIC connections.
type transport struct {
	privKey      ic.PrivKey
	localPeer    peer.ID
	identity     *p2ptls.Identity
	connManager  *connManager
	serverConfig *quic.Config
	clientConfig *quic.Config
	gater        connmgr.ConnectionGater
	rcmgr        network.ResourceManager

	holePunchingMx sync.Mutex
	holePunching   map[holePunchKey]*activeHolePunch

	connMx sync.Mutex
	conns  map[quic.Connection]*conn
}

var _ tpt.Transport = &transport{}

type holePunchKey struct {
	addr string
	peer peer.ID
}

type activeHolePunch struct {
	connCh    chan tpt.CapableConn
	fulfilled bool
}

// NewTransport creates a new QUIC transport
func NewTransport(key ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager) (tpt.Transport, error) {
	if len(psk) > 0 {
		log.Error("QUIC doesn't support private networks yet.")
		return nil, errors.New("QUIC doesn't support private networks yet")
	}
	localPeer, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return nil, err
	}
	identity, err := p2ptls.NewIdentity(key)
	if err != nil {
		return nil, err
	}
	connManager, err := newConnManager()
	if err != nil {
		return nil, err
	}
	if rcmgr == nil {
		rcmgr = network.NullResourceManager
	}
	config := quicConfig.Clone()
	keyBytes, err := key.Raw()
	if err != nil {
		return nil, err
	}
	keyReader := hkdf.New(sha256.New, keyBytes, nil, []byte(statelessResetKeyInfo))
	config.StatelessResetKey = make([]byte, 32)
	if _, err := io.ReadFull(keyReader, config.StatelessResetKey); err != nil {
		return nil, err
	}
	config.Tracer = tracer

	tr := &transport{
		privKey:      key,
		localPeer:    localPeer,
		identity:     identity,
		connManager:  connManager,
		gater:        gater,
		rcmgr:        rcmgr,
		conns:        make(map[quic.Connection]*conn),
		holePunching: make(map[holePunchKey]*activeHolePunch),
	}
	config.AllowConnectionWindowIncrease = tr.allowWindowIncrease
	tr.serverConfig = config
	tr.clientConfig = config.Clone()
	return tr, nil
}

// Dial dials a new QUIC connection
func (t *transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (tpt.CapableConn, error) {
	netw, host, err := manet.DialArgs(raddr)
	if err != nil {
		return nil, err
	}
	addr, err := net.ResolveUDPAddr(netw, host)
	if err != nil {
		return nil, err
	}
	remoteMultiaddr, err := toQuicMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	tlsConf, keyCh := t.identity.ConfigForPeer(p)
	if ok, isClient, _ := network.GetSimultaneousConnect(ctx); ok && !isClient {
		return t.holePunch(ctx, netw, addr, p)
	}

	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		log.Debugw("resource manager blocked outgoing connection", "peer", p, "addr", raddr, "error", err)
		return nil, err
	}
	if err := scope.SetPeer(p); err != nil {
		log.Debugw("resource manager blocked outgoing connection for peer", "peer", p, "addr", raddr, "error", err)
		scope.Done()
		return nil, err
	}
	pconn, err := t.connManager.Dial(netw, addr)
	if err != nil {
		return nil, err
	}
	qconn, err := quicDialContext(ctx, pconn, addr, host, tlsConf, t.clientConfig)
	if err != nil {
		scope.Done()
		pconn.DecreaseCount()
		return nil, err
	}
	// Should be ready by this point, don't block.
	var remotePubKey ic.PubKey
	select {
	case remotePubKey = <-keyCh:
	default:
	}
	if remotePubKey == nil {
		pconn.DecreaseCount()
		scope.Done()
		return nil, errors.New("p2p/transport/quic BUG: expected remote pub key to be set")
	}

	localMultiaddr, err := toQuicMultiaddr(pconn.LocalAddr())
	if err != nil {
		qconn.CloseWithError(0, "")
		return nil, err
	}
	c := &conn{
		quicConn:        qconn,
		pconn:           pconn,
		transport:       t,
		scope:           scope,
		privKey:         t.privKey,
		localPeer:       t.localPeer,
		localMultiaddr:  localMultiaddr,
		remotePubKey:    remotePubKey,
		remotePeerID:    p,
		remoteMultiaddr: remoteMultiaddr,
	}
	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, c) {
		qconn.CloseWithError(errorCodeConnectionGating, "connection gated")
		return nil, fmt.Errorf("secured connection gated")
	}
	t.addConn(qconn, c)
	return c, nil
}

func (t *transport) addConn(conn quic.Connection, c *conn) {
	t.connMx.Lock()
	t.conns[conn] = c
	t.connMx.Unlock()
}

func (t *transport) removeConn(conn quic.Connection) {
	t.connMx.Lock()
	delete(t.conns, conn)
	t.connMx.Unlock()
}

func (t *transport) holePunch(ctx context.Context, network string, addr *net.UDPAddr, p peer.ID) (tpt.CapableConn, error) {
	pconn, err := t.connManager.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	defer pconn.DecreaseCount()

	ctx, cancel := context.WithTimeout(ctx, HolePunchTimeout)
	defer cancel()

	key := holePunchKey{addr: addr.String(), peer: p}
	t.holePunchingMx.Lock()
	if _, ok := t.holePunching[key]; ok {
		t.holePunchingMx.Unlock()
		return nil, fmt.Errorf("already punching hole for %s", addr)
	}
	connCh := make(chan tpt.CapableConn, 1)
	t.holePunching[key] = &activeHolePunch{connCh: connCh}
	t.holePunchingMx.Unlock()

	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	payload := make([]byte, 64)
	var punchErr error
loop:
	for i := 0; ; i++ {
		if _, err := rand.Read(payload); err != nil {
			punchErr = err
			break
		}
		if _, err := pconn.UDPConn.WriteToUDP(payload, addr); err != nil {
			punchErr = err
			break
		}

		maxSleep := 10 * (i + 1) * (i + 1) // in ms
		if maxSleep > 200 {
			maxSleep = 200
		}
		d := 10*time.Millisecond + time.Duration(rand.Intn(maxSleep))*time.Millisecond
		if timer == nil {
			timer = time.NewTimer(d)
		} else {
			timer.Reset(d)
		}
		select {
		case c := <-connCh:
			t.holePunchingMx.Lock()
			delete(t.holePunching, key)
			t.holePunchingMx.Unlock()
			return c, nil
		case <-timer.C:
		case <-ctx.Done():
			punchErr = ErrHolePunching
			break loop
		}
	}
	// we only arrive here if punchErr != nil
	t.holePunchingMx.Lock()
	defer func() {
		delete(t.holePunching, key)
		t.holePunchingMx.Unlock()
	}()
	select {
	case c := <-t.holePunching[key].connCh:
		return c, nil
	default:
		return nil, punchErr
	}
}

// Don't use mafmt.QUIC as we don't want to dial DNS addresses. Just /ip{4,6}/udp/quic
var dialMatcher = mafmt.And(mafmt.IP, mafmt.Base(ma.P_UDP), mafmt.Base(ma.P_QUIC))

// CanDial determines if we can dial to an address
func (t *transport) CanDial(addr ma.Multiaddr) bool {
	return dialMatcher.Matches(addr)
}

// Listen listens for new QUIC connections on the passed multiaddr.
func (t *transport) Listen(addr ma.Multiaddr) (tpt.Listener, error) {
	lnet, host, err := manet.DialArgs(addr)
	if err != nil {
		return nil, err
	}
	laddr, err := net.ResolveUDPAddr(lnet, host)
	if err != nil {
		return nil, err
	}
	conn, err := t.connManager.Listen(lnet, laddr)
	if err != nil {
		return nil, err
	}
	ln, err := newListener(conn, t, t.localPeer, t.privKey, t.identity, t.rcmgr)
	if err != nil {
		conn.DecreaseCount()
		return nil, err
	}
	return ln, nil
}

func (t *transport) allowWindowIncrease(conn quic.Connection, size uint64) bool {
	// If the QUIC connection tries to increase the window before we've inserted it
	// into our connections map (which we do right after dialing / accepting it),
	// we have no way to account for that memory. This should be very rare.
	// Block this attempt. The connection can request more memory later.
	t.connMx.Lock()
	c, ok := t.conns[conn]
	t.connMx.Unlock()
	if !ok {
		return false
	}
	return c.allowWindowIncrease(size)
}

// Proxy returns true if this transport proxies.
func (t *transport) Proxy() bool {
	return false
}

// Protocols returns the set of protocols handled by this transport.
func (t *transport) Protocols() []int {
	return []int{ma.P_QUIC}
}

func (t *transport) String() string {
	return "QUIC"
}

func (t *transport) Close() error {
	return t.connManager.Close()
}
