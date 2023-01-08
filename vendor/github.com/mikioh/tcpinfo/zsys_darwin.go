// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_darwin.go

package tcpinfo

const (
	sysTCP_CONNECTION_INFO = 0x106

	sysTCPCI_OPT_TIMESTAMPS = 0x1
	sysTCPCI_OPT_SACK       = 0x2
	sysTCPCI_OPT_WSCALE     = 0x4
	sysTCPCI_OPT_ECN        = 0x8

	SysFlagLossRecovery       SysFlags = 0x1
	SysFlagReorderingDetected SysFlags = 0x2

	sizeofTCPConnectionInfo = 0x70
)

type tcpConnectionInfo struct {
	State               uint8
	Snd_wscale          uint8
	Rcv_wscale          uint8
	X__pad1             uint8
	Options             uint32
	Flags               uint32
	Rto                 uint32
	Maxseg              uint32
	Snd_ssthresh        uint32
	Snd_cwnd            uint32
	Snd_wnd             uint32
	Snd_sbbytes         uint32
	Rcv_wnd             uint32
	Rttcur              uint32
	Srtt                uint32
	Rttvar              uint32
	Pad_cgo_0           [4]byte
	Txpackets           uint64
	Txbytes             uint64
	Txretransmitbytes   uint64
	Rxpackets           uint64
	Rxbytes             uint64
	Rxoutoforderbytes   uint64
	Txretransmitpackets uint64
}
