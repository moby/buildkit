// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcpinfo

import (
	"errors"
	"time"
	"unsafe"

	"github.com/mikioh/tcpopt"
)

var options = [soMax]option{
	soInfo: {ianaProtocolTCP, sysTCP_CONNECTION_INFO, parseInfo},
}

// Marshal implements the Marshal method of tcpopt.Option interface.
func (i *Info) Marshal() ([]byte, error) {
	return (*[sizeofTCPConnectionInfo]byte)(unsafe.Pointer(i))[:], nil
}

type SysFlags uint

func (f SysFlags) String() string {
	s := ""
	for i, name := range []string{
		"loss recovery",
		"reordering detected",
	} {
		if f&(1<<uint(i)) != 0 {
			if s != "" {
				s += "|"
			}
			s += name
		}
	}
	if s == "" {
		s = "0"
	}
	return s
}

// A SysInfo represents platform-specific information.
type SysInfo struct {
	Flags                   SysFlags      `json:"flags"`          // flags
	SenderWindow            uint          `json:"snd_wnd"`        // advertised sender window in bytes
	SenderInUse             uint          `json:"snd_inuse"`      // bytes in send buffer including inflight data
	SRTT                    time.Duration `json:"srtt"`           // smoothed round-trip time
	SegsSent                uint64        `json:"segs_sent"`      // # of segements sent
	BytesSent               uint64        `json:"bytes_sent"`     // # of bytes sent
	RetransBytes            uint64        `json:"retrans_bytes"`  // # of retransmitted bytes
	SegsReceived            uint64        `json:"segs_rcvd"`      // # of segments received
	BytesReceived           uint64        `json:"bytes_rcvd"`     // # of bytes received
	OutOfOrderBytesReceived uint64        `json:"ooo_bytes_rcvd"` // # of our-of-order bytes received
	RetransSegs             uint64        `json:"retrans_segs"`   // # of retransmitted segments
}

var sysStates = [11]State{Closed, Listen, SynSent, SynReceived, Established, CloseWait, FinWait1, Closing, LastAck, FinWait2, TimeWait}

const sizeofTCPConnectionInfoV15 = 0x68

func parseInfo(b []byte) (tcpopt.Option, error) {
	if len(b) < sizeofTCPConnectionInfoV15 {
		return nil, errors.New("short buffer")
	}
	tci := (*tcpConnectionInfo)(unsafe.Pointer(&b[0]))
	i := &Info{State: sysStates[tci.State]}
	if tci.Options&sysTCPCI_OPT_WSCALE != 0 {
		i.Options = append(i.Options, WindowScale(tci.Snd_wscale))
		i.PeerOptions = append(i.PeerOptions, WindowScale(tci.Rcv_wscale))
	}
	if tci.Options&sysTCPCI_OPT_SACK != 0 {
		i.Options = append(i.Options, SACKPermitted(true))
		i.PeerOptions = append(i.PeerOptions, SACKPermitted(true))
	}
	if tci.Options&sysTCPCI_OPT_TIMESTAMPS != 0 {
		i.Options = append(i.Options, Timestamps(true))
		i.PeerOptions = append(i.PeerOptions, Timestamps(true))
	}
	i.SenderMSS = MaxSegSize(tci.Maxseg)
	i.ReceiverMSS = MaxSegSize(tci.Maxseg)
	i.RTT = time.Duration(tci.Rttcur) * time.Millisecond
	i.RTTVar = time.Duration(tci.Rttvar) * time.Millisecond
	i.RTO = time.Duration(tci.Rto) * time.Millisecond
	i.FlowControl = &FlowControl{
		ReceiverWindow: uint(tci.Rcv_wnd),
	}
	i.CongestionControl = &CongestionControl{
		SenderSSThreshold: uint(tci.Snd_ssthresh),
		SenderWindowBytes: uint(tci.Snd_cwnd),
	}
	i.Sys = &SysInfo{
		Flags:                   SysFlags(tci.Flags),
		SenderWindow:            uint(tci.Snd_wnd),
		SenderInUse:             uint(tci.Snd_sbbytes),
		SRTT:                    time.Duration(tci.Srtt) * time.Millisecond,
		SegsSent:                uint64(tci.Txpackets),
		BytesSent:               uint64(tci.Txbytes),
		RetransBytes:            uint64(tci.Txretransmitbytes),
		SegsReceived:            uint64(tci.Rxpackets),
		BytesReceived:           uint64(tci.Rxbytes),
		OutOfOrderBytesReceived: uint64(tci.Rxoutoforderbytes),
	}
	if len(b) > sizeofTCPConnectionInfoV15 {
		i.Sys.RetransSegs = uint64(tci.Txretransmitpackets)
	}
	return i, nil
}

func parseCCAlgorithmInfo(name string, b []byte) (CCAlgorithmInfo, error) {
	return nil, errors.New("operation not supported")
}
