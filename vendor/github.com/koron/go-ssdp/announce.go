package ssdp

import (
	"bytes"
	"fmt"
	"net"
)

// AnnounceAlive sends ssdp:alive message.
func AnnounceAlive(nt, usn, location, server string, maxAge int, localAddr string) error {
	// dial multicast UDP packet.
	conn, err := multicastListen(&udpAddrResolver{addr: localAddr})
	if err != nil {
		return err
	}
	defer conn.Close()
	// build and send message.
	addr, err := multicastSendAddr()
	if err != nil {
		return err
	}
	msg, err := buildAlive(addr, nt, usn, location, server, maxAge)
	if err != nil {
		return err
	}
	if _, err := conn.WriteTo(msg, addr); err != nil {
		return err
	}
	return nil
}

func buildAlive(raddr net.Addr, nt, usn, location, server string, maxAge int) ([]byte, error) {
	b := new(bytes.Buffer)
	// FIXME: error should be checked.
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	fmt.Fprintf(b, "HOST: %s\r\n", raddr.String())
	fmt.Fprintf(b, "NT: %s\r\n", nt)
	fmt.Fprintf(b, "NTS: %s\r\n", "ssdp:alive")
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

// AnnounceBye sends ssdp:byebye message.
func AnnounceBye(nt, usn, localAddr string) error {
	// dial multicast UDP packet.
	conn, err := multicastListen(&udpAddrResolver{addr: localAddr})
	if err != nil {
		return err
	}
	defer conn.Close()
	// build and send message.
	addr, err := multicastSendAddr()
	if err != nil {
		return err
	}
	msg, err := buildBye(addr, nt, usn)
	if err != nil {
		return err
	}
	if _, err := conn.WriteTo(msg, addr); err != nil {
		return err
	}
	return nil
}

func buildBye(raddr net.Addr, nt, usn string) ([]byte, error) {
	b := new(bytes.Buffer)
	// FIXME: error should be checked.
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	fmt.Fprintf(b, "HOST: %s\r\n", raddr.String())
	fmt.Fprintf(b, "NT: %s\r\n", nt)
	fmt.Fprintf(b, "NTS: %s\r\n", "ssdp:byebye")
	fmt.Fprintf(b, "USN: %s\r\n", usn)
	b.WriteString("\r\n")
	return b.Bytes(), nil
}
