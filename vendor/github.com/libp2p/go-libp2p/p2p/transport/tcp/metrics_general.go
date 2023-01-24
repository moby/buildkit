//go:build !linux && !darwin && !windows

package tcp

import "github.com/mikioh/tcpinfo"

const (
	hasSegmentCounter = false
	hasByteCounter    = false
)

func getSegmentsSent(info *tcpinfo.Info) uint64 { return 0 }
func getSegmentsRcvd(info *tcpinfo.Info) uint64 { return 0 }
func getBytesSent(info *tcpinfo.Info) uint64    { return 0 }
func getBytesRcvd(info *tcpinfo.Info) uint64    { return 0 }
