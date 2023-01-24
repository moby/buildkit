// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcpinfo

// A State represents a state of connection.
type State int

const (
	Unknown State = iota
	Closed
	Listen
	SynSent
	SynReceived
	Established
	FinWait1
	FinWait2
	CloseWait
	LastAck
	Closing
	TimeWait
)

var states = map[State]string{
	Unknown:     "unknown",
	Closed:      "closed",
	Listen:      "listen",
	SynSent:     "syn-sent",
	SynReceived: "syn-received",
	Established: "established",
	FinWait1:    "fin-wait-1",
	FinWait2:    "fin-wait-2",
	CloseWait:   "close-wait",
	LastAck:     "last-ack",
	Closing:     "closing",
	TimeWait:    "time-wait",
}

func (st State) String() string {
	s, ok := states[st]
	if !ok {
		return "<nil>"
	}
	return s
}

// An OptionKind represents an option kind.
type OptionKind int

const (
	KindMaxSegSize    OptionKind = 2
	KindWindowScale   OptionKind = 3
	KindSACKPermitted OptionKind = 4
	KindTimestamps    OptionKind = 8
)

var optionKinds = map[OptionKind]string{
	KindMaxSegSize:    "mss",
	KindWindowScale:   "wscale",
	KindSACKPermitted: "sack",
	KindTimestamps:    "tmstamps",
}

func (k OptionKind) String() string {
	s, ok := optionKinds[k]
	if !ok {
		return "<nil>"
	}
	return s
}

// An Option represents an option.
type Option interface {
	Kind() OptionKind
}

// A MaxSegSize represents a maxiumum segment size option.
type MaxSegSize uint

// Kind returns an option kind field.
func (mss MaxSegSize) Kind() OptionKind { return KindMaxSegSize }

// A WindowScale represents a windows scale option.
type WindowScale int

// Kind returns an option kind field.
func (ws WindowScale) Kind() OptionKind { return KindWindowScale }

// A SACKPermitted reports whether a selective acknowledgment
// permitted option is enabled.
type SACKPermitted bool

// Kind returns an option kind field.
func (sp SACKPermitted) Kind() OptionKind { return KindSACKPermitted }

// A Timestamps reports whether a timestamps option is enabled.
type Timestamps bool

// Kind returns an option kind field.
func (ts Timestamps) Kind() OptionKind { return KindTimestamps }
