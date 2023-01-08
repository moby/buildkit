package config

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	csms "github.com/libp2p/go-libp2p/p2p/net/conn-security-multistream"
)

// SecC is a security transport constructor.
type SecC func(h host.Host) (sec.SecureTransport, error)

// MsSecC is a tuple containing a security transport constructor and a protocol
// ID.
type MsSecC struct {
	SecC
	ID string
}

var securityArgTypes = newArgTypeSet(
	hostType, networkType, peerIDType,
	privKeyType, pubKeyType, pstoreType,
)

// SecurityConstructor creates a security constructor from the passed parameter
// using reflection.
func SecurityConstructor(security interface{}) (SecC, error) {
	// Already constructed?
	if t, ok := security.(sec.SecureTransport); ok {
		return func(_ host.Host) (sec.SecureTransport, error) {
			return t, nil
		}, nil
	}

	ctor, err := makeConstructor(security, securityType, securityArgTypes)
	if err != nil {
		return nil, err
	}
	return func(h host.Host) (sec.SecureTransport, error) {
		t, err := ctor(h, nil, nil, nil, nil)
		if err != nil {
			return nil, err
		}
		return t.(sec.SecureTransport), nil
	}, nil
}

func makeInsecureTransport(id peer.ID, privKey crypto.PrivKey) sec.SecureMuxer {
	secMuxer := new(csms.SSMuxer)
	secMuxer.AddTransport(insecure.ID, insecure.NewWithIdentity(id, privKey))
	return secMuxer
}

func makeSecurityMuxer(h host.Host, tpts []MsSecC) (sec.SecureMuxer, error) {
	secMuxer := new(csms.SSMuxer)
	transportSet := make(map[string]struct{}, len(tpts))
	for _, tptC := range tpts {
		if _, ok := transportSet[tptC.ID]; ok {
			return nil, fmt.Errorf("duplicate security transport: %s", tptC.ID)
		}
		transportSet[tptC.ID] = struct{}{}
	}
	for _, tptC := range tpts {
		tpt, err := tptC.SecC(h)
		if err != nil {
			return nil, err
		}
		if _, ok := tpt.(*insecure.Transport); ok {
			return nil, fmt.Errorf("cannot construct libp2p with an insecure transport, set the Insecure config option instead")
		}
		secMuxer.AddTransport(tptC.ID, tpt)
	}
	return secMuxer, nil
}
