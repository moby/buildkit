package ssdp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// Service is discovered service.
type Service struct {
	// Type is a property of "ST"
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

var rxMaxAge = regexp.MustCompile(`\bmax-age\s*=\s*(\d+)\b`)

func extractMaxAge(s string, value int) int {
	v := value
	if m := rxMaxAge.FindStringSubmatch(s); m != nil {
		i64, err := strconv.ParseInt(m[1], 10, 32)
		if err == nil {
			v = int(i64)
		}
	}
	return v
}

// MaxAge extracts "max-age" value from "CACHE-CONTROL" property.
func (s *Service) MaxAge() int {
	if s.maxAge == nil {
		s.maxAge = new(int)
		*s.maxAge = extractMaxAge(s.rawHeader.Get("CACHE-CONTROL"), -1)
	}
	return *s.maxAge
}

// Header returns all properties in response of search.
func (s *Service) Header() http.Header {
	return s.rawHeader
}

const (
	// All is a search type to search all services and devices.
	All = "ssdp:all"

	// RootDevice is a search type to search UPnP root devices.
	RootDevice = "upnp:rootdevice"
)

// Search searches services by SSDP.
func Search(searchType string, waitSec int, localAddr string) ([]Service, error) {
	// dial multicast UDP packet.
	conn, err := multicastListen(&udpAddrResolver{addr: localAddr})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	logf("search on %s", conn.LocalAddr().String())

	// send request.
	addr, err := multicastSendAddr()
	if err != nil {
		return nil, err
	}
	msg, err := buildSearch(addr, searchType, waitSec)
	if err != nil {
		return nil, err
	}
	if _, err := conn.WriteTo(msg, addr); err != nil {
		return nil, err
	}

	// wait response.
	var list []Service
	h := func(a net.Addr, d []byte) error {
		srv, err := parseService(a, d)
		if err != nil {
			logf("invalid search response from %s: %s", a.String(), err)
			return nil
		}
		list = append(list, *srv)
		logf("search response from %s: %s", a.String(), srv.USN)
		return nil
	}
	d := time.Second * time.Duration(waitSec)
	if err := conn.readPackets(d, h); err != nil {
		return nil, err
	}

	return list, err
}

func buildSearch(raddr net.Addr, searchType string, waitSec int) ([]byte, error) {
	b := new(bytes.Buffer)
	// FIXME: error should be checked.
	b.WriteString("M-SEARCH * HTTP/1.1\r\n")
	fmt.Fprintf(b, "HOST: %s\r\n", raddr.String())
	fmt.Fprintf(b, "MAN: %q\r\n", "ssdp:discover")
	fmt.Fprintf(b, "MX: %d\r\n", waitSec)
	fmt.Fprintf(b, "ST: %s\r\n", searchType)
	b.WriteString("\r\n")
	return b.Bytes(), nil
}

var (
	errWithoutHTTPPrefix = errors.New("without HTTP prefix")
)

var endOfHeader = []byte{'\r', '\n', '\r', '\n'}

func parseService(addr net.Addr, data []byte) (*Service, error) {
	if !bytes.HasPrefix(data, []byte("HTTP")) {
		return nil, errWithoutHTTPPrefix
	}
	// Complement newlines on tail of header for buggy SSDP responses.
	if !bytes.HasSuffix(data, endOfHeader) {
		// why we should't use append() for this purpose:
		// https://play.golang.org/p/IM1pONW9lqm
		data = bytes.Join([][]byte{data, endOfHeader}, nil)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return &Service{
		Type:      resp.Header.Get("ST"),
		USN:       resp.Header.Get("USN"),
		Location:  resp.Header.Get("LOCATION"),
		Server:    resp.Header.Get("SERVER"),
		rawHeader: resp.Header,
	}, nil
}
