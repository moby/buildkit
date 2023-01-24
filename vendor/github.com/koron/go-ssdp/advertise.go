package ssdp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

type message struct {
	to   net.Addr
	data []byte
}

// Advertiser is a server to advertise a service.
type Advertiser struct {
	st       string
	usn      string
	location string
	server   string
	maxAge   int

	conn *multicastConn
	ch   chan *message
	wg   sync.WaitGroup
	wgS  sync.WaitGroup
}

// Advertise starts advertisement of service.
func Advertise(st, usn, location, server string, maxAge int) (*Advertiser, error) {
	conn, err := multicastListen(recvAddrResolver)
	if err != nil {
		return nil, err
	}
	logf("SSDP advertise on: %s", conn.LocalAddr().String())
	a := &Advertiser{
		st:       st,
		usn:      usn,
		location: location,
		server:   server,
		maxAge:   maxAge,
		conn:     conn,
		ch:       make(chan *message),
	}
	a.wg.Add(2)
	a.wgS.Add(1)
	go func() {
		a.sendMain()
		a.wgS.Done()
		a.wg.Done()
	}()
	go func() {
		a.recvMain()
		a.wg.Done()
	}()
	return a, nil
}

func (a *Advertiser) recvMain() error {
	err := a.conn.readPackets(0, func(addr net.Addr, data []byte) error {
		if err := a.handleRaw(addr, data); err != nil {
			logf("failed to handle message: %s", err)
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (a *Advertiser) sendMain() error {
	for msg := range a.ch {
		_, err := a.conn.WriteTo(msg.data, msg.to)
		if err != nil {
			if nerr, ok := err.(net.Error); !ok || !nerr.Temporary() {
				logf("failed to send: %s", err)
			}
		}
	}
	return nil
}

func (a *Advertiser) handleRaw(from net.Addr, raw []byte) error {
	if !bytes.HasPrefix(raw, []byte("M-SEARCH ")) {
		// unexpected method.
		return nil
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return err
	}
	var (
		man = req.Header.Get("MAN")
		st  = req.Header.Get("ST")
	)
	if man != `"ssdp:discover"` {
		return fmt.Errorf("unexpected MAN: %s", man)
	}
	if st != All && st != RootDevice && st != a.st {
		// skip when ST is not matched/expected.
		return nil
	}
	logf("received M-SEARCH MAN=%s ST=%s from %s", man, st, from.String())
	// build and send a response.
	msg, err := buildOK(a.st, a.usn, a.location, a.server, a.maxAge)
	if err != nil {
		return err
	}
	a.ch <- &message{to: from, data: msg}
	return nil
}

func buildOK(st, usn, location, server string, maxAge int) ([]byte, error) {
	b := new(bytes.Buffer)
	// FIXME: error should be checked.
	b.WriteString("HTTP/1.1 200 OK\r\n")
	fmt.Fprintf(b, "EXT: \r\n")
	fmt.Fprintf(b, "ST: %s\r\n", st)
	fmt.Fprintf(b, "USN: %s\r\n", usn)
	if location != "" {
		fmt.Fprintf(b, "LOCATION: %s\r\n", location)
	}
	if server != "" {
		fmt.Fprintf(b, "SERVER: %s\r\n", server)
	}
	fmt.Fprintf(b, "CACHE-CONTROL: max-age=%d\r\n", maxAge)
	b.WriteString("\r\n")
	return b.Bytes(), nil
}

// Close stops advertisement.
func (a *Advertiser) Close() error {
	if a.conn != nil {
		// closing order is very important. be careful to change:
		// stop sending loop by closing the channel and wait it.
		close(a.ch)
		a.wgS.Wait()
		// stop receiving loop by closing the connection.
		a.conn.Close()
		a.wg.Wait()
		a.conn = nil
	}
	return nil
}

// Alive announces ssdp:alive message.
func (a *Advertiser) Alive() error {
	addr, err := multicastSendAddr()
	if err != nil {
		return err
	}
	msg, err := buildAlive(addr, a.st, a.usn, a.location, a.server,
		a.maxAge)
	if err != nil {
		return err
	}
	a.ch <- &message{to: addr, data: msg}
	logf("sent alive")
	return nil
}

// Bye announces ssdp:byebye message.
func (a *Advertiser) Bye() error {
	addr, err := multicastSendAddr()
	if err != nil {
		return err
	}
	msg, err := buildBye(addr, a.st, a.usn)
	if err != nil {
		return err
	}
	a.ch <- &message{to: addr, data: msg}
	logf("sent bye")
	return nil
}
