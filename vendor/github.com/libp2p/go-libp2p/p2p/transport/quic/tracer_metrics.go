package libp2pquic

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/logging"
)

var (
	bytesTransferred *prometheus.CounterVec
	newConns         *prometheus.CounterVec
	closedConns      *prometheus.CounterVec
	sentPackets      *prometheus.CounterVec
	rcvdPackets      *prometheus.CounterVec
	bufferedPackets  *prometheus.CounterVec
	droppedPackets   *prometheus.CounterVec
	lostPackets      *prometheus.CounterVec
	connErrors       *prometheus.CounterVec
)

type aggregatingCollector struct {
	mutex sync.Mutex

	conns         map[string] /* conn ID */ *metricsConnTracer
	rtts          prometheus.Histogram
	connDurations prometheus.Histogram
}

func newAggregatingCollector() *aggregatingCollector {
	return &aggregatingCollector{
		conns: make(map[string]*metricsConnTracer),
		rtts: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "quic_smoothed_rtt",
			Help:    "Smoothed RTT",
			Buckets: prometheus.ExponentialBuckets(0.001, 1.25, 40), // 1ms to ~6000ms
		}),
		connDurations: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "quic_connection_duration",
			Help:    "Connection Duration",
			Buckets: prometheus.ExponentialBuckets(1, 1.5, 40), // 1s to ~12 weeks
		}),
	}
}

var _ prometheus.Collector = &aggregatingCollector{}

func (c *aggregatingCollector) Describe(descs chan<- *prometheus.Desc) {
	descs <- c.rtts.Desc()
	descs <- c.connDurations.Desc()
}

func (c *aggregatingCollector) Collect(metrics chan<- prometheus.Metric) {
	now := time.Now()
	c.mutex.Lock()
	for _, conn := range c.conns {
		if rtt, valid := conn.getSmoothedRTT(); valid {
			c.rtts.Observe(rtt.Seconds())
		}
		c.connDurations.Observe(now.Sub(conn.startTime).Seconds())
	}
	c.mutex.Unlock()
	metrics <- c.rtts
	metrics <- c.connDurations
}

func (c *aggregatingCollector) AddConn(id string, t *metricsConnTracer) {
	c.mutex.Lock()
	c.conns[id] = t
	c.mutex.Unlock()
}

func (c *aggregatingCollector) RemoveConn(id string) {
	c.mutex.Lock()
	delete(c.conns, id)
	c.mutex.Unlock()
}

var collector *aggregatingCollector

func init() {
	const (
		direction = "direction"
		encLevel  = "encryption_level"
	)

	closedConns = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_connections_closed_total",
			Help: "closed QUIC connection",
		},
		[]string{direction},
	)
	prometheus.MustRegister(closedConns)
	newConns = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_connections_new_total",
			Help: "new QUIC connection",
		},
		[]string{direction, "handshake_successful"},
	)
	prometheus.MustRegister(newConns)
	bytesTransferred = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_transferred_bytes",
			Help: "QUIC bytes transferred",
		},
		[]string{direction}, // TODO: this is confusing. Other times, we use direction for the perspective
	)
	prometheus.MustRegister(bytesTransferred)
	sentPackets = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_packets_sent_total",
			Help: "QUIC packets sent",
		},
		[]string{encLevel},
	)
	prometheus.MustRegister(sentPackets)
	rcvdPackets = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_packets_rcvd_total",
			Help: "QUIC packets received",
		},
		[]string{encLevel},
	)
	prometheus.MustRegister(rcvdPackets)
	bufferedPackets = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_packets_buffered_total",
			Help: "Buffered packets",
		},
		[]string{"packet_type"},
	)
	prometheus.MustRegister(bufferedPackets)
	droppedPackets = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_packets_dropped_total",
			Help: "Dropped packets",
		},
		[]string{"packet_type", "reason"},
	)
	prometheus.MustRegister(droppedPackets)
	connErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_connection_errors_total",
			Help: "QUIC connection errors",
		},
		[]string{"side", "error_code"},
	)
	prometheus.MustRegister(connErrors)
	lostPackets = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "quic_packets_lost_total",
			Help: "QUIC lost received",
		},
		[]string{encLevel, "reason"},
	)
	prometheus.MustRegister(lostPackets)
	collector = newAggregatingCollector()
	prometheus.MustRegister(collector)
}

type metricsTracer struct{}

var _ logging.Tracer = &metricsTracer{}

func (m *metricsTracer) TracerForConnection(_ context.Context, p logging.Perspective, connID logging.ConnectionID) logging.ConnectionTracer {
	return &metricsConnTracer{perspective: p, connID: connID}
}

func (m *metricsTracer) SentPacket(_ net.Addr, _ *logging.Header, size logging.ByteCount, _ []logging.Frame) {
	bytesTransferred.WithLabelValues("sent").Add(float64(size))
}

func (m *metricsTracer) DroppedPacket(addr net.Addr, packetType logging.PacketType, count logging.ByteCount, reason logging.PacketDropReason) {
}

type metricsConnTracer struct {
	perspective       logging.Perspective
	startTime         time.Time
	connID            logging.ConnectionID
	handshakeComplete bool

	mutex              sync.Mutex
	numRTTMeasurements int
	rtt                time.Duration
}

var _ logging.ConnectionTracer = &metricsConnTracer{}

func (m *metricsConnTracer) getDirection() string {
	if m.perspective == logging.PerspectiveClient {
		return "outgoing"
	}
	return "incoming"
}

func (m *metricsConnTracer) getEncLevel(packetType logging.PacketType) string {
	switch packetType {
	case logging.PacketType0RTT:
		return "0-RTT"
	case logging.PacketTypeInitial:
		return "Initial"
	case logging.PacketTypeHandshake:
		return "Handshake"
	case logging.PacketTypeRetry:
		return "Retry"
	case logging.PacketType1RTT:
		return "1-RTT"
	default:
		return "unknown"
	}
}

func (m *metricsConnTracer) StartedConnection(net.Addr, net.Addr, logging.ConnectionID, logging.ConnectionID) {
	m.startTime = time.Now()
	collector.AddConn(m.connID.String(), m)
}

func (m *metricsConnTracer) NegotiatedVersion(chosen quic.VersionNumber, clientVersions []quic.VersionNumber, serverVersions []quic.VersionNumber) {
}

func (m *metricsConnTracer) ClosedConnection(e error) {
	var (
		applicationErr      *quic.ApplicationError
		transportErr        *quic.TransportError
		statelessResetErr   *quic.StatelessResetError
		vnErr               *quic.VersionNegotiationError
		idleTimeoutErr      *quic.IdleTimeoutError
		handshakeTimeoutErr *quic.HandshakeTimeoutError
		remote              bool
		desc                string
	)

	switch {
	case errors.As(e, &applicationErr):
		return
	case errors.As(e, &transportErr):
		remote = transportErr.Remote
		desc = transportErr.ErrorCode.String()
	case errors.As(e, &statelessResetErr):
		remote = true
		desc = "stateless_reset"
	case errors.As(e, &vnErr):
		desc = "version_negotiation"
	case errors.As(e, &idleTimeoutErr):
		desc = "idle_timeout"
	case errors.As(e, &handshakeTimeoutErr):
		desc = "handshake_timeout"
	default:
		desc = fmt.Sprintf("unknown error: %v", e)
	}

	side := "local"
	if remote {
		side = "remote"
	}
	connErrors.WithLabelValues(side, desc).Inc()
}
func (m *metricsConnTracer) SentTransportParameters(parameters *logging.TransportParameters)     {}
func (m *metricsConnTracer) ReceivedTransportParameters(parameters *logging.TransportParameters) {}
func (m *metricsConnTracer) RestoredTransportParameters(parameters *logging.TransportParameters) {}
func (m *metricsConnTracer) SentPacket(hdr *logging.ExtendedHeader, size logging.ByteCount, _ *logging.AckFrame, _ []logging.Frame) {
	bytesTransferred.WithLabelValues("sent").Add(float64(size))
	sentPackets.WithLabelValues(m.getEncLevel(logging.PacketTypeFromHeader(&hdr.Header))).Inc()
}

func (m *metricsConnTracer) ReceivedVersionNegotiationPacket(hdr *logging.Header, v []logging.VersionNumber) {
	bytesTransferred.WithLabelValues("rcvd").Add(float64(hdr.ParsedLen() + logging.ByteCount(4*len(v))))
	rcvdPackets.WithLabelValues("Version Negotiation").Inc()
}

func (m *metricsConnTracer) ReceivedRetry(*logging.Header) {
	rcvdPackets.WithLabelValues("Retry").Inc()
}

func (m *metricsConnTracer) ReceivedPacket(hdr *logging.ExtendedHeader, size logging.ByteCount, _ []logging.Frame) {
	bytesTransferred.WithLabelValues("rcvd").Add(float64(size))
	rcvdPackets.WithLabelValues(m.getEncLevel(logging.PacketTypeFromHeader(&hdr.Header))).Inc()
}

func (m *metricsConnTracer) BufferedPacket(packetType logging.PacketType) {
	bufferedPackets.WithLabelValues(m.getEncLevel(packetType)).Inc()
}

func (m *metricsConnTracer) DroppedPacket(packetType logging.PacketType, size logging.ByteCount, r logging.PacketDropReason) {
	bytesTransferred.WithLabelValues("rcvd").Add(float64(size))
	var reason string
	switch r {
	case logging.PacketDropKeyUnavailable:
		reason = "key_unavailable"
	case logging.PacketDropUnknownConnectionID:
		reason = "unknown_connection_id"
	case logging.PacketDropHeaderParseError:
		reason = "header_parse_error"
	case logging.PacketDropPayloadDecryptError:
		reason = "payload_decrypt_error"
	case logging.PacketDropProtocolViolation:
		reason = "protocol_violation"
	case logging.PacketDropDOSPrevention:
		reason = "dos_prevention"
	case logging.PacketDropUnsupportedVersion:
		reason = "unsupported_version"
	case logging.PacketDropUnexpectedPacket:
		reason = "unexpected_packet"
	case logging.PacketDropUnexpectedSourceConnectionID:
		reason = "unexpected_source_connection_id"
	case logging.PacketDropUnexpectedVersion:
		reason = "unexpected_version"
	case logging.PacketDropDuplicate:
		reason = "duplicate"
	default:
		reason = "unknown"
	}
	droppedPackets.WithLabelValues(m.getEncLevel(packetType), reason).Inc()
}

func (m *metricsConnTracer) UpdatedMetrics(rttStats *logging.RTTStats, cwnd, bytesInFlight logging.ByteCount, packetsInFlight int) {
	m.mutex.Lock()
	m.rtt = rttStats.SmoothedRTT()
	m.numRTTMeasurements++
	m.mutex.Unlock()
}

func (m *metricsConnTracer) AcknowledgedPacket(logging.EncryptionLevel, logging.PacketNumber) {}

func (m *metricsConnTracer) LostPacket(level logging.EncryptionLevel, _ logging.PacketNumber, r logging.PacketLossReason) {
	var reason string
	switch r {
	case logging.PacketLossReorderingThreshold:
		reason = "reordering_threshold"
	case logging.PacketLossTimeThreshold:
		reason = "time_threshold"
	default:
		reason = "unknown"
	}
	lostPackets.WithLabelValues(level.String(), reason).Inc()
}

func (m *metricsConnTracer) UpdatedCongestionState(state logging.CongestionState) {}
func (m *metricsConnTracer) UpdatedPTOCount(value uint32)                         {}
func (m *metricsConnTracer) UpdatedKeyFromTLS(level logging.EncryptionLevel, perspective logging.Perspective) {
}
func (m *metricsConnTracer) UpdatedKey(generation logging.KeyPhase, remote bool) {}
func (m *metricsConnTracer) DroppedEncryptionLevel(level logging.EncryptionLevel) {
	if level == logging.EncryptionHandshake {
		m.handleHandshakeComplete()
	}
}
func (m *metricsConnTracer) DroppedKey(generation logging.KeyPhase) {}
func (m *metricsConnTracer) SetLossTimer(timerType logging.TimerType, level logging.EncryptionLevel, time time.Time) {
}

func (m *metricsConnTracer) LossTimerExpired(timerType logging.TimerType, level logging.EncryptionLevel) {
}
func (m *metricsConnTracer) LossTimerCanceled() {}

func (m *metricsConnTracer) Close() {
	if m.handshakeComplete {
		closedConns.WithLabelValues(m.getDirection()).Inc()
	} else {
		newConns.WithLabelValues(m.getDirection(), "false").Inc()
	}
	collector.RemoveConn(m.connID.String())
}

func (m *metricsConnTracer) Debug(name, msg string) {}

func (m *metricsConnTracer) handleHandshakeComplete() {
	m.handshakeComplete = true
	newConns.WithLabelValues(m.getDirection(), "true").Inc()
}

func (m *metricsConnTracer) getSmoothedRTT() (rtt time.Duration, valid bool) {
	m.mutex.Lock()
	rtt = m.rtt
	valid = m.numRTTMeasurements > 10
	m.mutex.Unlock()
	return
}
