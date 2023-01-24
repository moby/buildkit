//go:build linux

package tcp

import "github.com/mikioh/tcpinfo"

const (
	hasSegmentCounter = true
	hasByteCounter    = false
)

func getSegmentsSent(info *tcpinfo.Info) uint64 { return uint64(info.Sys.SegsOut) }
func getSegmentsRcvd(info *tcpinfo.Info) uint64 { return uint64(info.Sys.SegsIn) }
func getBytesSent(info *tcpinfo.Info) uint64    { return 0 }
func getBytesRcvd(info *tcpinfo.Info) uint64    { return 0 }
