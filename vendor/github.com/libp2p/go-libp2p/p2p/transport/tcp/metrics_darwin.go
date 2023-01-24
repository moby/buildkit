//go:build darwin

package tcp

import "github.com/mikioh/tcpinfo"

const (
	hasSegmentCounter = true
	hasByteCounter    = true
)

func getSegmentsSent(info *tcpinfo.Info) uint64 { return info.Sys.SegsSent }
func getSegmentsRcvd(info *tcpinfo.Info) uint64 { return info.Sys.SegsReceived }
func getBytesSent(info *tcpinfo.Info) uint64    { return info.Sys.BytesSent }
func getBytesRcvd(info *tcpinfo.Info) uint64    { return info.Sys.BytesReceived }
