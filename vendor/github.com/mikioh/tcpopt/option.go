// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcpopt

import "time"

// An Option represents a socket option.
type Option interface {
	// Level returns the platform-specific socket option level.
	Level() int

	// Name returns the platform-specific socket option name.
	Name() int

	// Marshal returns the binary encoding of socket option.
	Marshal() ([]byte, error)
}

// NoDelay specifies the use of Nagle's algorithm.
type NoDelay bool

// Level implements the Level method of Option interface.
func (nd NoDelay) Level() int { return options[soNodelay].level }

// Name implements the Name method of Option interface.
func (nd NoDelay) Name() int { return options[soNodelay].name }

// MSS specifies the maximum segment size.
type MSS int

// Level implements the Level method of Option interface.
func (mss MSS) Level() int { return options[soMaxseg].level }

// Name implements the Name method of Option interface.
func (mss MSS) Name() int { return options[soMaxseg].name }

// SendBuffer specifies the size of send buffer.
type SendBuffer int

// Level implements the Level method of Option interface.
func (sb SendBuffer) Level() int { return options[soSndbuf].level }

// Name implements the Name method of Option interface.
func (sb SendBuffer) Name() int { return options[soSndbuf].name }

// ReceiveBuffer specifies the size of receive buffer.
type ReceiveBuffer int

// Level implements the Level method of Option interface.
func (rb ReceiveBuffer) Level() int { return options[soRcvbuf].level }

// Name implements the Name method of Option interface.
func (rb ReceiveBuffer) Name() int { return options[soRcvbuf].name }

// KeepAlive specifies the use of keep alive.
type KeepAlive bool

// Level implements the Level method of Option interface.
func (ka KeepAlive) Level() int { return options[soKeepalive].level }

// Name implements the Name method of Option interface.
func (ka KeepAlive) Name() int { return options[soKeepalive].name }

// KeepAliveIdleInterval is the idle interval until the first probe is
// sent.
//
// OpenBSD doesn't support this option.
// See TCP_KEEPIDLE or TCP_KEEPALIVE for further information.
type KeepAliveIdleInterval time.Duration

// Level implements the Level method of Option interface.
func (ka KeepAliveIdleInterval) Level() int { return options[soKeepidle].level }

// Name implements the Name method of Option interface.
func (ka KeepAliveIdleInterval) Name() int { return options[soKeepidle].name }

// KeepAliveProbeInterval is the interval between keepalive probes.
//
// OpenBSD doesn't support this option.
// See TCP_KEEPINTVL for further information.
type KeepAliveProbeInterval time.Duration

// Level implements the Level method of Option interface.
func (ka KeepAliveProbeInterval) Level() int { return options[soKeepintvl].level }

// Name implements the Name method of Option interface.
func (ka KeepAliveProbeInterval) Name() int { return options[soKeepintvl].name }

// KeepAliveProbeCount is the number of keepalive probes should be
// repeated when the peer is not responding.
//
// OpenBSD and Windows don't support this option.
// See TCP_KEEPCNT for further information.
type KeepAliveProbeCount int

// Level implements the Level method of Option interface.
func (ka KeepAliveProbeCount) Level() int { return options[soKeepcnt].level }

// Name implements the Name method of Option interface.
func (ka KeepAliveProbeCount) Name() int { return options[soKeepcnt].name }

// Cork specifies the use of TCP_CORK or TCP_NOPUSH option.
//
// On DragonFly BSD, the caller may need to adjust the
// net.inet.tcp.disable_nopush kernel state.
// NetBSD and Windows don't support this option.
type Cork bool

// Level implements the Level method of Option interface.
func (ck Cork) Level() int { return options[soCork].level }

// Name implements the Name method of Option interface.
func (ck Cork) Name() int { return options[soCork].name }

// NotSentLowWMK specifies the amount of unsent bytes in transmission
// queue. The network poller such as kqueue or epoll doesn't report
// that the connection is writable while the amount of unsent data
// size is greater than NotSentLowWMK.
//
// Only Darwin and Linux support this option.
// See TCP_NOTSENT_LOWAT for further information.
type NotSentLowWMK int

// Level implements the Level method of Option interface.
func (ns NotSentLowWMK) Level() int { return options[soNotsentLOWAT].level }

// Name implements the Name method of Option interface.
func (ns NotSentLowWMK) Name() int { return options[soNotsentLOWAT].name }

// Error represents an error on the socket.
type Error int

// Level implements the Level method of Option interface.
func (e Error) Level() int { return options[soError].level }

// Name implements the Name method of Option interface.
func (e Error) Name() int { return options[soError].name }

// ECN specifies the use of ECN.
//
// Only Darwin supports this option.
type ECN bool

// Level implements the Level method of Option interface.
func (cn ECN) Level() int { return options[soECN].level }

// Name implements the Name method of Option interface.
func (cn ECN) Name() int { return options[soECN].name }
