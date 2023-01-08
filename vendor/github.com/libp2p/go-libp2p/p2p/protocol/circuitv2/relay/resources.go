package relay

import (
	"time"
)

// Resources are the resource limits associated with the relay service.
type Resources struct {
	// Limit is the (optional) relayed connection limits.
	Limit *RelayLimit

	// ReservationTTL is the duration of a new (or refreshed reservation).
	// Defaults to 1hr.
	ReservationTTL time.Duration

	// MaxReservations is the maximum number of active relay slots; defaults to 128.
	MaxReservations int
	// MaxCircuits is the maximum number of open relay connections for each peer; defaults to 16.
	MaxCircuits int
	// BufferSize is the size of the relayed connection buffers; defaults to 2048.
	BufferSize int

	// MaxReservationsPerPeer is the maximum number of reservations originating from the same
	// peer; default is 4.
	MaxReservationsPerPeer int
	// MaxReservationsPerIP is the maximum number of reservations originating from the same
	// IP address; default is 8.
	MaxReservationsPerIP int
	// MaxReservationsPerASN is the maximum number of reservations origination from the same
	// ASN; default is 32
	MaxReservationsPerASN int
}

// RelayLimit are the per relayed connection resource limits.
type RelayLimit struct {
	// Duration is the time limit before resetting a relayed connection; defaults to 2min.
	Duration time.Duration
	// Data is the limit of data relayed (on each direction) before resetting the connection.
	// Defaults to 128KB
	Data int64
}

// DefaultResources returns a Resources object with the default filled in.
func DefaultResources() Resources {
	return Resources{
		Limit: DefaultLimit(),

		ReservationTTL: time.Hour,

		MaxReservations: 128,
		MaxCircuits:     16,
		BufferSize:      2048,

		MaxReservationsPerPeer: 4,
		MaxReservationsPerIP:   8,
		MaxReservationsPerASN:  32,
	}
}

// DefaultLimit returns a RelayLimit object with the defaults filled in.
func DefaultLimit() *RelayLimit {
	return &RelayLimit{
		Duration: 2 * time.Minute,
		Data:     1 << 17, // 128K
	}
}
