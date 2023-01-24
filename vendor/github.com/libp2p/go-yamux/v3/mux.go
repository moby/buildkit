package yamux

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// Config is used to tune the Yamux session
type Config struct {
	// AcceptBacklog is used to limit how many streams may be
	// waiting an accept.
	AcceptBacklog int

	// PingBacklog is used to limit how many ping acks we can queue.
	PingBacklog int

	// EnableKeepalive is used to do a period keep alive
	// messages using a ping.
	EnableKeepAlive bool

	// KeepAliveInterval is how often to perform the keep alive
	KeepAliveInterval time.Duration

	// ConnectionWriteTimeout is meant to be a "safety valve" timeout after
	// we which will suspect a problem with the underlying connection and
	// close it. This is only applied to writes, where's there's generally
	// an expectation that things will move along quickly.
	ConnectionWriteTimeout time.Duration

	// MaxIncomingStreams is maximum number of concurrent incoming streams
	// that we accept. If the peer tries to open more streams, those will be
	// reset immediately.
	MaxIncomingStreams uint32

	// InitialStreamWindowSize is used to control the initial
	// window size that we allow for a stream.
	InitialStreamWindowSize uint32

	// MaxStreamWindowSize is used to control the maximum
	// window size that we allow for a stream.
	MaxStreamWindowSize uint32

	// LogOutput is used to control the log destination
	LogOutput io.Writer

	// ReadBufSize controls the size of the read buffer.
	//
	// Set to 0 to disable it.
	ReadBufSize int

	// WriteCoalesceDelay is the maximum amount of time we'll delay
	// coalescing a packet before sending it. This should be on the order of
	// micro-milliseconds.
	WriteCoalesceDelay time.Duration

	// MaxMessageSize is the maximum size of a message that we'll send on a
	// stream. This ensures that a single stream doesn't hog a connection.
	MaxMessageSize uint32
}

// DefaultConfig is used to return a default configuration
func DefaultConfig() *Config {
	return &Config{
		AcceptBacklog:           256,
		PingBacklog:             32,
		EnableKeepAlive:         true,
		KeepAliveInterval:       30 * time.Second,
		ConnectionWriteTimeout:  10 * time.Second,
		MaxIncomingStreams:      1000,
		InitialStreamWindowSize: initialStreamWindow,
		MaxStreamWindowSize:     maxStreamWindow,
		LogOutput:               os.Stderr,
		ReadBufSize:             4096,
		MaxMessageSize:          64 * 1024,
		WriteCoalesceDelay:      100 * time.Microsecond,
	}
}

// VerifyConfig is used to verify the sanity of configuration
func VerifyConfig(config *Config) error {
	if config.AcceptBacklog <= 0 {
		return fmt.Errorf("backlog must be positive")
	}
	if config.KeepAliveInterval == 0 {
		return fmt.Errorf("keep-alive interval must be positive")
	}
	if config.InitialStreamWindowSize < initialStreamWindow {
		return errors.New("InitialStreamWindowSize must be larger or equal 256 kB")
	}
	if config.MaxStreamWindowSize < config.InitialStreamWindowSize {
		return errors.New("MaxStreamWindowSize must be larger than the InitialStreamWindowSize")
	}
	if config.MaxMessageSize < 1024 {
		return fmt.Errorf("MaxMessageSize must be greater than a kilobyte")
	}
	if config.WriteCoalesceDelay < 0 {
		return fmt.Errorf("WriteCoalesceDelay must be >= 0")
	}
	if config.PingBacklog < 1 {
		return fmt.Errorf("PingBacklog must be > 0")
	}
	return nil
}

// Server is used to initialize a new server-side connection.
// There must be at most one server-side connection. If a nil config is
// provided, the DefaultConfiguration will be used.
func Server(conn net.Conn, config *Config, mm MemoryManager) (*Session, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if err := VerifyConfig(config); err != nil {
		return nil, err
	}
	return newSession(config, conn, false, config.ReadBufSize, mm), nil
}

// Client is used to initialize a new client-side connection.
// There must be at most one client-side connection.
func Client(conn net.Conn, config *Config, mm MemoryManager) (*Session, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if err := VerifyConfig(config); err != nil {
		return nil, err
	}
	return newSession(config, conn, true, config.ReadBufSize, mm), nil
}
