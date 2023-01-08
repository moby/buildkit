// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build freebsd netbsd

package tcpinfo

import (
	"errors"
	"runtime"
	"time"
	"unsafe"

	"github.com/mikioh/tcpopt"
)

var options = [soMax]option{
	soInfo: {ianaProtocolTCP, sysTCP_INFO, parseInfo},
}

// Marshal implements the Marshal method of tcpopt.Option interface.
func (i *Info) Marshal() ([]byte, error) { return (*[sizeofTCPInfo]byte)(unsafe.Pointer(i))[:], nil }

// A SysInfo represents platform-specific information.
type SysInfo struct {
	SenderWindowBytes uint `json:"snd_wnd_bytes"`   // advertised sender window in bytes [FreeBSD]
	SenderWindowSegs  uint `json:"snd_wnd_segs"`    // advertised sender window in # of segments [NetBSD]
	NextEgressSeq     uint `json:"egress_seq"`      // next egress seq. number
	NextIngressSeq    uint `json:"ingress_seq"`     // next ingress seq. number
	RetransSegs       uint `json:"retrans_segs"`    // # of retransmit segments sent
	OutOfOrderSegs    uint `json:"ooo_segs"`        // # of out-of-order segments received
	ZeroWindowUpdates uint `json:"zerownd_updates"` // # of zero-window updates sent
	Offloading        bool `json:"offloading"`      // TCP offload processing
}

var sysStates = [11]State{Closed, Listen, SynSent, SynReceived, Established, CloseWait, FinWait1, Closing, LastAck, FinWait2, TimeWait}

func parseInfo(b []byte) (tcpopt.Option, error) {
	if len(b) < sizeofTCPInfo {
		return nil, errors.New("short buffer")
	}
	ti := (*tcpInfo)(unsafe.Pointer(&b[0]))
	i := &Info{State: sysStates[ti.State]}
	if ti.Options&sysTCPI_OPT_WSCALE != 0 {
		i.Options = append(i.Options, WindowScale(ti.Pad_cgo_0[0]>>4))
		i.PeerOptions = append(i.PeerOptions, WindowScale(ti.Pad_cgo_0[0]&0x0f))
	}
	if ti.Options&sysTCPI_OPT_SACK != 0 {
		i.Options = append(i.Options, SACKPermitted(true))
		i.PeerOptions = append(i.PeerOptions, SACKPermitted(true))
	}
	if ti.Options&sysTCPI_OPT_TIMESTAMPS != 0 {
		i.Options = append(i.Options, Timestamps(true))
		i.PeerOptions = append(i.PeerOptions, Timestamps(true))
	}
	i.SenderMSS = MaxSegSize(ti.Snd_mss)
	i.ReceiverMSS = MaxSegSize(ti.Rcv_mss)
	i.RTT = time.Duration(ti.Rtt) * time.Microsecond
	i.RTTVar = time.Duration(ti.Rttvar) * time.Microsecond
	i.RTO = time.Duration(ti.Rto) * time.Microsecond
	i.ATO = time.Duration(ti.X__tcpi_ato) * time.Microsecond
	i.LastDataSent = time.Duration(ti.X__tcpi_last_data_sent) * time.Microsecond
	i.LastDataReceived = time.Duration(ti.Last_data_recv) * time.Microsecond
	i.LastAckReceived = time.Duration(ti.X__tcpi_last_ack_recv) * time.Microsecond
	i.FlowControl = &FlowControl{
		ReceiverWindow: uint(ti.Rcv_space),
	}
	i.CongestionControl = &CongestionControl{
		SenderSSThreshold:   uint(ti.Snd_ssthresh),
		ReceiverSSThreshold: uint(ti.X__tcpi_rcv_ssthresh),
	}
	i.Sys = &SysInfo{
		NextEgressSeq:     uint(ti.Snd_nxt),
		NextIngressSeq:    uint(ti.Rcv_nxt),
		RetransSegs:       uint(ti.Snd_rexmitpack),
		OutOfOrderSegs:    uint(ti.Rcv_ooopack),
		ZeroWindowUpdates: uint(ti.Snd_zerowin),
	}
	if ti.Options&sysTCPI_OPT_TOE != 0 {
		i.Sys.Offloading = true
	}
	switch runtime.GOOS {
	case "freebsd":
		i.CongestionControl.SenderWindowBytes = uint(ti.Snd_cwnd)
		i.Sys.SenderWindowBytes = uint(ti.Snd_wnd)
	case "netbsd":
		i.CongestionControl.SenderWindowSegs = uint(ti.Snd_cwnd)
		i.Sys.SenderWindowSegs = uint(ti.Snd_wnd)
	}
	return i, nil
}

func parseCCAlgorithmInfo(name string, b []byte) (CCAlgorithmInfo, error) {
	return nil, errors.New("operation not supported")
}
