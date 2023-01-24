// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcpinfo

import (
	"errors"
	"strings"
	"time"
	"unsafe"

	"github.com/mikioh/tcpopt"
)

var options = [soMax]option{
	soInfo:   {ianaProtocolTCP, sysTCP_INFO, parseInfo},
	soCCInfo: {ianaProtocolTCP, sysTCP_CC_INFO, parseCCInfo},
	soCCAlgo: {ianaProtocolTCP, sysTCP_CONGESTION, parseCCAlgorithm},
}

// Marshal implements the Marshal method of tcpopt.Option interface.
func (i *Info) Marshal() ([]byte, error) { return (*[sizeofTCPInfo]byte)(unsafe.Pointer(i))[:], nil }

// A CAState represents a state of congestion avoidance.
type CAState int

var caStates = map[CAState]string{
	CAOpen:     "open",
	CADisorder: "disorder",
	CACWR:      "congestion window reduced",
	CARecovery: "recovery",
	CALoss:     "loss",
}

func (st CAState) String() string {
	s, ok := caStates[st]
	if !ok {
		return "<nil>"
	}
	return s
}

// A SysInfo represents platform-specific information.
type SysInfo struct {
	PathMTU                 uint          `json:"path_mtu"`           // path maximum transmission unit
	AdvertisedMSS           MaxSegSize    `json:"adv_mss"`            // advertised maximum segment size
	CAState                 CAState       `json:"ca_state"`           // state of congestion avoidance
	Retransmissions         uint          `json:"rexmits"`            // # of retranmissions on timeout invoked
	Backoffs                uint          `json:"backoffs"`           // # of times retransmission backoff timer invoked
	WindowOrKeepAliveProbes uint          `json:"wnd_ka_probes"`      // # of window or keep alive probes sent
	UnackedSegs             uint          `json:"unacked_segs"`       // # of unack'd segments
	SackedSegs              uint          `json:"sacked_segs"`        // # of sack'd segments
	LostSegs                uint          `json:"lost_segs"`          // # of lost segments
	RetransSegs             uint          `json:"retrans_segs"`       // # of retransmitting segments in transmission queue
	ForwardAckSegs          uint          `json:"fack_segs"`          // # of forward ack segments in transmission queue
	ReorderedSegs           uint          `json:"reord_segs"`         // # of reordered segments allowed
	ReceiverRTT             time.Duration `json:"rcv_rtt"`            // current RTT for receiver
	TotalRetransSegs        uint          `json:"total_retrans_segs"` // # of retransmitted segments
	PacingRate              uint64        `json:"pacing_rate"`        // pacing rate
	ThruBytesAcked          uint64        `json:"thru_bytes_acked"`   // # of bytes for which cumulative acknowledgments have been received
	ThruBytesReceived       uint64        `json:"thru_bytes_rcvd"`    // # of bytes for which cumulative acknowledgments have been sent
	SegsOut                 uint          `json:"segs_out"`           // # of segments sent
	SegsIn                  uint          `json:"segs_in"`            // # of segments received
	NotSentBytes            uint          `json:"not_sent_bytes"`     // # of bytes not sent yet
	MinRTT                  time.Duration `json:"min_rtt"`            // current measured minimum RTT; zero means not available
	DataSegsOut             uint          `json:"data_segs_out"`      // # of segments sent containing a positive length data segment
	DataSegsIn              uint          `json:"data_segs_in"`       // # of segments received containing a positive length data segment
}

var sysStates = [12]State{Unknown, Established, SynSent, SynReceived, FinWait1, FinWait2, TimeWait, Closed, CloseWait, LastAck, Listen, Closing}

const (
	sizeofTCPInfoV4_9    = 0xa0
	sizeofTCPInfoV3_19   = 0x78
	sizeofTCPInfoV2_6_10 = 0x57
)

func parseInfo(b []byte) (tcpopt.Option, error) {
	if len(b) < sizeofTCPInfoV4_9 {
		return parseInfo3_19(b)
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
	i.ATO = time.Duration(ti.Ato) * time.Microsecond
	i.LastDataSent = time.Duration(ti.Last_data_sent) * time.Millisecond
	i.LastDataReceived = time.Duration(ti.Last_data_recv) * time.Millisecond
	i.LastAckReceived = time.Duration(ti.Last_ack_recv) * time.Millisecond
	i.FlowControl = &FlowControl{
		ReceiverWindow: uint(ti.Rcv_space),
	}
	i.CongestionControl = &CongestionControl{
		SenderSSThreshold:   uint(ti.Snd_ssthresh),
		ReceiverSSThreshold: uint(ti.Rcv_ssthresh),
		SenderWindowSegs:    uint(ti.Snd_cwnd),
	}
	i.Sys = &SysInfo{
		PathMTU:                 uint(ti.Pmtu),
		AdvertisedMSS:           MaxSegSize(ti.Advmss),
		CAState:                 CAState(ti.Ca_state),
		Retransmissions:         uint(ti.Retransmits),
		Backoffs:                uint(ti.Backoff),
		WindowOrKeepAliveProbes: uint(ti.Probes),
		UnackedSegs:             uint(ti.Unacked),
		SackedSegs:              uint(ti.Sacked),
		LostSegs:                uint(ti.Lost),
		RetransSegs:             uint(ti.Retrans),
		ForwardAckSegs:          uint(ti.Fackets),
		ReorderedSegs:           uint(ti.Reordering),
		ReceiverRTT:             time.Duration(ti.Rcv_rtt) * time.Microsecond,
		TotalRetransSegs:        uint(ti.Total_retrans),
		PacingRate:              uint64(ti.Pacing_rate),
		ThruBytesAcked:          uint64(ti.Bytes_acked),
		ThruBytesReceived:       uint64(ti.Bytes_received),
		SegsIn:                  uint(ti.Segs_in),
		SegsOut:                 uint(ti.Segs_out),
		NotSentBytes:            uint(ti.Notsent_bytes),
		MinRTT:                  time.Duration(ti.Min_rtt) * time.Microsecond,
		DataSegsIn:              uint(ti.Data_segs_in),
		DataSegsOut:             uint(ti.Data_segs_out),
	}
	return i, nil
}

func parseInfo3_19(b []byte) (tcpopt.Option, error) {
	if len(b) < sizeofTCPInfoV3_19 {
		return parseInfo2_6_10(b)
	}
	ti := (*tcpInfo3_19)(unsafe.Pointer(&b[0]))
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
	i.ATO = time.Duration(ti.Ato) * time.Microsecond
	i.LastDataSent = time.Duration(ti.Last_data_sent) * time.Millisecond
	i.LastDataReceived = time.Duration(ti.Last_data_recv) * time.Millisecond
	i.LastAckReceived = time.Duration(ti.Last_ack_recv) * time.Millisecond
	i.FlowControl = &FlowControl{
		ReceiverWindow: uint(ti.Rcv_space),
	}
	i.CongestionControl = &CongestionControl{
		SenderSSThreshold:   uint(ti.Snd_ssthresh),
		ReceiverSSThreshold: uint(ti.Rcv_ssthresh),
		SenderWindowSegs:    uint(ti.Snd_cwnd),
	}
	i.Sys = &SysInfo{
		PathMTU:                 uint(ti.Pmtu),
		AdvertisedMSS:           MaxSegSize(ti.Advmss),
		CAState:                 CAState(ti.Ca_state),
		Retransmissions:         uint(ti.Retransmits),
		Backoffs:                uint(ti.Backoff),
		WindowOrKeepAliveProbes: uint(ti.Probes),
		UnackedSegs:             uint(ti.Unacked),
		SackedSegs:              uint(ti.Sacked),
		LostSegs:                uint(ti.Lost),
		RetransSegs:             uint(ti.Retrans),
		ForwardAckSegs:          uint(ti.Fackets),
		ReorderedSegs:           uint(ti.Reordering),
		ReceiverRTT:             time.Duration(ti.Rcv_rtt) * time.Microsecond,
		TotalRetransSegs:        uint(ti.Total_retrans),
		PacingRate:              uint64(ti.Pacing_rate),
	}
	return i, nil
}

func parseInfo2_6_10(b []byte) (tcpopt.Option, error) {
	if len(b) < sizeofTCPInfoV2_6_10 {
		return nil, errors.New("short buffer")
	}
	ti := (*tcpInfo2_6_10)(unsafe.Pointer(&b[0]))
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
	i.ATO = time.Duration(ti.Ato) * time.Microsecond
	i.LastDataSent = time.Duration(ti.Last_data_sent) * time.Millisecond
	i.LastDataReceived = time.Duration(ti.Last_data_recv) * time.Millisecond
	i.LastAckReceived = time.Duration(ti.Last_ack_recv) * time.Millisecond
	i.FlowControl = &FlowControl{
		ReceiverWindow: uint(ti.Rcv_space),
	}
	i.CongestionControl = &CongestionControl{
		SenderSSThreshold:   uint(ti.Snd_ssthresh),
		ReceiverSSThreshold: uint(ti.Rcv_ssthresh),
		SenderWindowSegs:    uint(ti.Snd_cwnd),
	}
	i.Sys = &SysInfo{
		PathMTU:                 uint(ti.Pmtu),
		AdvertisedMSS:           MaxSegSize(ti.Advmss),
		CAState:                 CAState(ti.Ca_state),
		Retransmissions:         uint(ti.Retransmits),
		Backoffs:                uint(ti.Backoff),
		WindowOrKeepAliveProbes: uint(ti.Probes),
		UnackedSegs:             uint(ti.Unacked),
		SackedSegs:              uint(ti.Sacked),
		LostSegs:                uint(ti.Lost),
		RetransSegs:             uint(ti.Retrans),
		ForwardAckSegs:          uint(ti.Fackets),
		ReorderedSegs:           uint(ti.Reordering),
		ReceiverRTT:             time.Duration(ti.Rcv_rtt) * time.Microsecond,
		TotalRetransSegs:        uint(ti.Total_retrans),
	}
	return i, nil
}

// A VegasInfo represents Vegas congestion control information.
type VegasInfo struct {
	Enabled    bool          `json:"enabled"`
	RoundTrips uint          `json:"rnd_trips"` // # of round-trips
	RTT        time.Duration `json:"rtt"`       // round-trip time
	MinRTT     time.Duration `json:"min_rtt"`   // minimum round-trip time
}

// Algorithm implements the Algorithm method of CCAlgorithmInfo
// interface.
func (vi *VegasInfo) Algorithm() string { return "vegas" }

// A CEState represents a state of ECN congestion encountered (CE)
// codepoint.
type CEState int

// A DCTCPInfo represents Datacenter TCP congestion control
// information.
type DCTCPInfo struct {
	Enabled         bool    `json:"enabled"`
	CEState         CEState `json:"ce_state"`    // state of ECN CE codepoint
	Alpha           uint    `json:"alpha"`       // fraction of bytes sent
	ECNAckedBytes   uint    `json:"ecn_acked"`   // # of acked bytes with ECN
	TotalAckedBytes uint    `json:"total_acked"` // total # of acked bytes
}

// Algorithm implements the Algorithm method of CCAlgorithmInfo
// interface.
func (di *DCTCPInfo) Algorithm() string { return "dctcp" }

// A BBRInfo represents Bottleneck Bandwidth and Round-trip
// propagation time-based congestion control information.
type BBRInfo struct {
	MaxBW          uint64        `json:"max_bw"`      // maximum-filtered bandwidth in bps
	MinRTT         time.Duration `json:"min_rtt"`     // minimum-filtered round-trip time
	PacingGain     uint          `json:"pacing_gain"` // pacing gain shifted left 8 bits
	CongWindowGain uint          `json:"cwnd_gain"`   // congestion window gain shifted left 8 bits
}

// Algorithm implements the Algorithm method of CCAlgorithmInfo
// interface.
func (bi *BBRInfo) Algorithm() string { return "bbr" }

func parseCCAlgorithmInfo(name string, b []byte) (CCAlgorithmInfo, error) {
	if strings.HasPrefix(name, "dctcp") {
		if len(b) < sizeofTCPDCTCPInfo {
			return nil, errors.New("short buffer")
		}
		sdi := (*tcpDCTCPInfo)(unsafe.Pointer(&b[0]))
		di := &DCTCPInfo{Alpha: uint(sdi.Alpha)}
		if sdi.Enabled != 0 {
			di.Enabled = true
		}
		return di, nil
	}
	if strings.HasPrefix(name, "bbr") {
		if len(b) < sizeofTCPBBRInfo {
			return nil, errors.New("short buffer")
		}
		sbi := (*tcpBBRInfo)(unsafe.Pointer(&b[0]))
		return &BBRInfo{
			MaxBW:          uint64(sbi.Bw_hi)<<32 | uint64(sbi.Bw_lo),
			MinRTT:         time.Duration(sbi.Min_rtt) * time.Microsecond,
			PacingGain:     uint(sbi.Pacing_gain),
			CongWindowGain: uint(sbi.Cwnd_gain),
		}, nil
	}
	if len(b) < sizeofTCPVegasInfo {
		return nil, errors.New("short buffer")
	}
	svi := (*tcpVegasInfo)(unsafe.Pointer(&b[0]))
	vi := &VegasInfo{
		RoundTrips: uint(svi.Rttcnt),
		RTT:        time.Duration(svi.Rtt) * time.Microsecond,
		MinRTT:     time.Duration(svi.Minrtt) * time.Microsecond,
	}
	if svi.Enabled != 0 {
		vi.Enabled = true
	}
	return vi, nil
}
