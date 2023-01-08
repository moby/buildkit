package ssdp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

// Monitor monitors SSDP's alive and byebye messages.
type Monitor struct {
	Alive  AliveHandler
	Bye    ByeHandler
	Search SearchHandler

	conn *multicastConn
	wg   sync.WaitGroup
}

// Start starts to monitor SSDP messages.
func (m *Monitor) Start() error {
	conn, err := multicastListen(recvAddrResolver)
	if err != nil {
		return err
	}
	logf("monitoring on %s", conn.LocalAddr().String())
	m.conn = conn
	m.wg.Add(1)
	go func() {
		m.serve()
		m.wg.Done()
	}()
	return nil
}

func (m *Monitor) serve() error {
	err := m.conn.readPackets(0, func(addr net.Addr, data []byte) error {
		msg := make([]byte, len(data))
		copy(msg, data)
		go m.handleRaw(addr, msg)
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (m *Monitor) handleRaw(addr net.Addr, raw []byte) error {
	// Add newline to workaround buggy SSDP responses
	if !bytes.HasSuffix(raw, endOfHeader) {
		raw = bytes.Join([][]byte{raw, endOfHeader}, nil)
	}
	if bytes.HasPrefix(raw, []byte("M-SEARCH ")) {
		return m.handleSearch(addr, raw)
	}
	if bytes.HasPrefix(raw, []byte("NOTIFY ")) {
		return m.handleNotify(addr, raw)
	}
	n := bytes.Index(raw, []byte("\r\n"))
	logf("unexpected method: %q", string(raw[:n]))
	return nil
}

func (m *Monitor) handleNotify(addr net.Addr, raw []byte) error {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return err
	}
	switch nts := req.Header.Get("NTS"); nts {
	case "ssdp:alive":
		if req.Method != "NOTIFY" {
			return fmt.Errorf("unexpected method for %q: %s", "ssdp:alive", req.Method)
		}
		if h := m.Alive; h != nil {
			h(&AliveMessage{
				From:      addr,
				Type:      req.Header.Get("NT"),
				USN:       req.Header.Get("USN"),
				Location:  req.Header.Get("LOCATION"),
				Server:    req.Header.Get("SERVER"),
				rawHeader: req.Header,
			})
		}
	case "ssdp:byebye":
		if req.Method != "NOTIFY" {
			return fmt.Errorf("unexpected method for %q: %s", "ssdp:byebye", req.Method)
		}
		if h := m.Bye; h != nil {
			h(&ByeMessage{
				From:      addr,
				Type:      req.Header.Get("NT"),
				USN:       req.Header.Get("USN"),
				rawHeader: req.Header,
			})
		}
	default:
		return fmt.Errorf("unknown NTS: %s", nts)
	}
	return nil
}

func (m *Monitor) handleSearch(addr net.Addr, raw []byte) error {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return err
	}
	man := req.Header.Get("MAN")
	if man != `"ssdp:discover"` {
		return fmt.Errorf("unexpected MAN: %s", man)
	}
	if h := m.Search; h != nil {
		h(&SearchMessage{
			From:      addr,
			Type:      req.Header.Get("ST"),
			rawHeader: req.Header,
		})
	}
	return nil
}

// Close closes monitoring.
func (m *Monitor) Close() error {
	if m.conn != nil {
		m.conn.Close()
		m.conn = nil
		m.wg.Wait()
	}
	return nil
}

// AliveMessage represents SSDP's ssdp:alive message.
type AliveMessage struct {
	// From is a sender of this message
	From net.Addr

	// Type is a property of "NT"
	Type string

	// USN is a property of "USN"
	USN string

	// Location is a property of "LOCATION"
	Location string

	// Server is a property of "SERVER"
	Server string

	rawHeader http.Header
	maxAge    *int
}

// Header returns all properties in alive message.
func (m *AliveMessage) Header() http.Header {
	return m.rawHeader
}

// MaxAge extracts "max-age" value from "CACHE-CONTROL" property.
func (m *AliveMessage) MaxAge() int {
	if m.maxAge == nil {
		m.maxAge = new(int)
		*m.maxAge = extractMaxAge(m.rawHeader.Get("CACHE-CONTROL"), -1)
	}
	return *m.maxAge
}

// AliveHandler is handler of Alive message.
type AliveHandler func(*AliveMessage)

// ByeMessage represents SSDP's ssdp:byebye message.
type ByeMessage struct {
	// From is a sender of this message
	From net.Addr

	// Type is a property of "NT"
	Type string

	// USN is a property of "USN"
	USN string

	rawHeader http.Header
}

// Header returns all properties in bye message.
func (m *ByeMessage) Header() http.Header {
	return m.rawHeader
}

// ByeHandler is handler of Bye message.
type ByeHandler func(*ByeMessage)

// SearchMessage represents SSDP's ssdp:discover message.
type SearchMessage struct {
	From net.Addr
	Type string

	rawHeader http.Header
}

// Header returns all properties in search message.
func (s *SearchMessage) Header() http.Header {
	return s.rawHeader
}

// SearchHandler is handler of Search message.
type SearchHandler func(*SearchMessage)
