// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_linux.go

package tcpinfo

const (
	sysTCP_INFO       = 0xb
	sysTCP_CONGESTION = 0xd
	sysTCP_CC_INFO    = 0x1a

	sysTCPI_OPT_TIMESTAMPS = 0x1
	sysTCPI_OPT_SACK       = 0x2
	sysTCPI_OPT_WSCALE     = 0x4
	sysTCPI_OPT_ECN        = 0x8
	sysTCPI_OPT_ECN_SEEN   = 0x10
	sysTCPI_OPT_SYN_DATA   = 0x20

	CAOpen     CAState = 0x0
	CADisorder CAState = 0x1
	CACWR      CAState = 0x2
	CARecovery CAState = 0x3
	CALoss     CAState = 0x4

	sizeofTCPInfo      = 0xc0
	sizeofTCPCCInfo    = 0x14
	sizeofTCPVegasInfo = 0x10
	sizeofTCPDCTCPInfo = 0x10
	sizeofTCPBBRInfo   = 0x14
)

type tcpInfo struct {
	State           uint8
	Ca_state        uint8
	Retransmits     uint8
	Probes          uint8
	Backoff         uint8
	Options         uint8
	Pad_cgo_0       [1]byte
	Pad_cgo_1       [1]byte
	Rto             uint32
	Ato             uint32
	Snd_mss         uint32
	Rcv_mss         uint32
	Unacked         uint32
	Sacked          uint32
	Lost            uint32
	Retrans         uint32
	Fackets         uint32
	Last_data_sent  uint32
	Last_ack_sent   uint32
	Last_data_recv  uint32
	Last_ack_recv   uint32
	Pmtu            uint32
	Rcv_ssthresh    uint32
	Rtt             uint32
	Rttvar          uint32
	Snd_ssthresh    uint32
	Snd_cwnd        uint32
	Advmss          uint32
	Reordering      uint32
	Rcv_rtt         uint32
	Rcv_space       uint32
	Total_retrans   uint32
	Pacing_rate     uint64
	Max_pacing_rate uint64
	Bytes_acked     uint64
	Bytes_received  uint64
	Segs_out        uint32
	Segs_in         uint32
	Notsent_bytes   uint32
	Min_rtt         uint32
	Data_segs_in    uint32
	Data_segs_out   uint32
	Delivery_rate   uint64
	Busy_time       uint64
	Rwnd_limited    uint64
	Sndbuf_limited  uint64
}

type tcpCCInfo [20]byte

type tcpVegasInfo struct {
	Enabled uint32
	Rttcnt  uint32
	Rtt     uint32
	Minrtt  uint32
}

type tcpDCTCPInfo struct {
	Enabled  uint16
	Ce_state uint16
	Alpha    uint32
	Ab_ecn   uint32
	Ab_tot   uint32
}

type tcpBBRInfo struct {
	Bw_lo       uint32
	Bw_hi       uint32
	Min_rtt     uint32
	Pacing_gain uint32
	Cwnd_gain   uint32
}
