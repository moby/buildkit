package ssdp

import "log"

// Logger is default logger for SSDP module.
var Logger *log.Logger

func logf(s string, a ...interface{}) {
	if l := Logger; l != nil {
		l.Printf(s, a...)
	}
}
