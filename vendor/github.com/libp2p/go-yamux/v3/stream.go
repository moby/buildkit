package yamux

import (
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type streamState int

const (
	streamInit streamState = iota
	streamSYNSent
	streamSYNReceived
	streamEstablished
	streamFinished
)

type halfStreamState int

const (
	halfOpen halfStreamState = iota
	halfClosed
	halfReset
)

// Stream is used to represent a logical stream
// within a session.
type Stream struct {
	sendWindow uint32

	memory int

	id      uint32
	session *Session

	recvWindow uint32
	epochStart time.Time

	state                 streamState
	writeState, readState halfStreamState
	stateLock             sync.Mutex

	recvBuf segmentedBuffer

	recvNotifyCh chan struct{}
	sendNotifyCh chan struct{}

	readDeadline, writeDeadline pipeDeadline
}

// newStream is used to construct a new stream within a given session for an ID.
// It assumes that a memory allocation has been obtained for the initialWindow.
func newStream(session *Session, id uint32, state streamState, initialWindow uint32) *Stream {
	s := &Stream{
		id:            id,
		session:       session,
		state:         state,
		sendWindow:    initialStreamWindow,
		memory:        int(initialWindow),
		readDeadline:  makePipeDeadline(),
		writeDeadline: makePipeDeadline(),
		// Initialize the recvBuf with initialStreamWindow, not config.InitialStreamWindowSize.
		// The peer isn't allowed to send more data than initialStreamWindow until we've sent
		// the first window update (which will grant it up to config.InitialStreamWindowSize).
		recvBuf:      newSegmentedBuffer(initialWindow),
		recvWindow:   session.config.InitialStreamWindowSize,
		epochStart:   time.Now(),
		recvNotifyCh: make(chan struct{}, 1),
		sendNotifyCh: make(chan struct{}, 1),
	}
	return s
}

// Session returns the associated stream session
func (s *Stream) Session() *Session {
	return s.session
}

// StreamID returns the ID of this stream
func (s *Stream) StreamID() uint32 {
	return s.id
}

// Read is used to read from the stream
func (s *Stream) Read(b []byte) (n int, err error) {
START:
	s.stateLock.Lock()
	state := s.readState
	s.stateLock.Unlock()

	switch state {
	case halfOpen:
		// Open -> read
	case halfClosed:
		empty := s.recvBuf.Len() == 0
		if empty {
			return 0, io.EOF
		}
		// Closed, but we have data pending -> read.
	case halfReset:
		return 0, ErrStreamReset
	default:
		panic("unknown state")
	}

	// If there is no data available, block
	if s.recvBuf.Len() == 0 {
		select {
		case <-s.recvNotifyCh:
			goto START
		case <-s.readDeadline.wait():
			return 0, ErrTimeout
		}
	}

	// Read any bytes
	n, _ = s.recvBuf.Read(b)

	// Send a window update potentially
	err = s.sendWindowUpdate()
	return n, err
}

// Write is used to write to the stream
func (s *Stream) Write(b []byte) (int, error) {
	var total int
	for total < len(b) {
		n, err := s.write(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// write is used to write to the stream, may return on
// a short write.
func (s *Stream) write(b []byte) (n int, err error) {
	var flags uint16
	var max uint32
	var hdr header

START:
	s.stateLock.Lock()
	state := s.writeState
	s.stateLock.Unlock()

	switch state {
	case halfOpen:
		// Open for writing -> write
	case halfClosed:
		return 0, ErrStreamClosed
	case halfReset:
		return 0, ErrStreamReset
	default:
		panic("unknown state")
	}

	// If there is no data available, block
	window := atomic.LoadUint32(&s.sendWindow)
	if window == 0 {
		select {
		case <-s.sendNotifyCh:
			goto START
		case <-s.writeDeadline.wait():
			return 0, ErrTimeout
		}
	}

	// Determine the flags if any
	flags = s.sendFlags()

	// Send up to min(message, window
	max = min(window, s.session.config.MaxMessageSize-headerSize, uint32(len(b)))

	// Send the header
	hdr = encode(typeData, flags, s.id, max)
	if err = s.session.sendMsg(hdr, b[:max], s.writeDeadline.wait()); err != nil {
		return 0, err
	}

	// Reduce our send window
	atomic.AddUint32(&s.sendWindow, ^uint32(max-1))

	// Unlock
	return int(max), err
}

// sendFlags determines any flags that are appropriate
// based on the current stream state
func (s *Stream) sendFlags() uint16 {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	var flags uint16
	switch s.state {
	case streamInit:
		flags |= flagSYN
		s.state = streamSYNSent
	case streamSYNReceived:
		flags |= flagACK
		s.state = streamEstablished
	}
	return flags
}

// sendWindowUpdate potentially sends a window update enabling
// further writes to take place. Must be invoked with the lock.
func (s *Stream) sendWindowUpdate() error {
	// Determine the flags if any
	flags := s.sendFlags()

	// Update the receive window.
	needed, delta := s.recvBuf.GrowTo(s.recvWindow, flags != 0)
	if !needed {
		return nil
	}

	now := time.Now()
	if rtt := s.session.getRTT(); flags == 0 && rtt > 0 && now.Sub(s.epochStart) < rtt*4 {
		var recvWindow uint32
		if s.recvWindow > math.MaxUint32/2 {
			recvWindow = min(math.MaxUint32, s.session.config.MaxStreamWindowSize)
		} else {
			recvWindow = min(s.recvWindow*2, s.session.config.MaxStreamWindowSize)
		}
		if recvWindow > s.recvWindow {
			grow := recvWindow - s.recvWindow
			if err := s.session.memoryManager.ReserveMemory(int(grow), 128); err == nil {
				s.recvWindow = recvWindow
				s.memory += int(grow)
				_, delta = s.recvBuf.GrowTo(s.recvWindow, true)
			}
		}
	}

	s.epochStart = now
	hdr := encode(typeWindowUpdate, flags, s.id, delta)
	return s.session.sendMsg(hdr, nil, nil)
}

// sendClose is used to send a FIN
func (s *Stream) sendClose() error {
	flags := s.sendFlags()
	flags |= flagFIN
	hdr := encode(typeWindowUpdate, flags, s.id, 0)
	return s.session.sendMsg(hdr, nil, nil)
}

// sendReset is used to send a RST
func (s *Stream) sendReset() error {
	hdr := encode(typeWindowUpdate, flagRST, s.id, 0)
	return s.session.sendMsg(hdr, nil, nil)
}

// Reset resets the stream (forcibly closes the stream)
func (s *Stream) Reset() error {
	sendReset := false
	s.stateLock.Lock()
	switch s.state {
	case streamFinished:
		s.stateLock.Unlock()
		return nil
	case streamInit:
		// we haven't sent anything, so we don't need to send a reset.
	case streamSYNSent, streamSYNReceived, streamEstablished:
		sendReset = true
	default:
		panic("unhandled state")
	}

	// at least one direction is open, we need to reset.

	// If we've already sent/received an EOF, no need to reset that side.
	if s.writeState == halfOpen {
		s.writeState = halfReset
	}
	if s.readState == halfOpen {
		s.readState = halfReset
	}
	s.state = streamFinished
	s.notifyWaiting()
	s.stateLock.Unlock()
	if sendReset {
		_ = s.sendReset()
	}
	s.cleanup()
	return nil
}

// CloseWrite is used to close the stream for writing.
func (s *Stream) CloseWrite() error {
	s.stateLock.Lock()
	switch s.writeState {
	case halfOpen:
		// Open for writing -> close write
	case halfClosed:
		s.stateLock.Unlock()
		return nil
	case halfReset:
		s.stateLock.Unlock()
		return ErrStreamReset
	default:
		panic("invalid state")
	}
	s.writeState = halfClosed
	cleanup := s.readState != halfOpen
	if cleanup {
		s.state = streamFinished
	}
	s.stateLock.Unlock()
	s.notifyWaiting()

	err := s.sendClose()
	if cleanup {
		// we're fully closed, might as well be nice to the user and
		// free everything early.
		s.cleanup()
	}
	return err
}

// CloseRead is used to close the stream for writing.
func (s *Stream) CloseRead() error {
	cleanup := false
	s.stateLock.Lock()
	switch s.readState {
	case halfOpen:
		// Open for reading -> close read
	case halfClosed, halfReset:
		s.stateLock.Unlock()
		return nil
	default:
		panic("invalid state")
	}
	s.readState = halfReset
	cleanup = s.writeState != halfOpen
	if cleanup {
		s.state = streamFinished
	}
	s.stateLock.Unlock()
	s.notifyWaiting()
	if cleanup {
		// we're fully closed, might as well be nice to the user and
		// free everything early.
		s.cleanup()
	}
	return nil
}

// Close is used to close the stream.
func (s *Stream) Close() error {
	_ = s.CloseRead() // can't fail.
	return s.CloseWrite()
}

// forceClose is used for when the session is exiting
func (s *Stream) forceClose() {
	s.stateLock.Lock()
	if s.readState == halfOpen {
		s.readState = halfReset
	}
	if s.writeState == halfOpen {
		s.writeState = halfReset
	}
	s.state = streamFinished
	s.notifyWaiting()
	s.stateLock.Unlock()

	s.readDeadline.set(time.Time{})
	s.writeDeadline.set(time.Time{})
}

// called when fully closed to release any system resources.
func (s *Stream) cleanup() {
	s.session.closeStream(s.id)
	s.readDeadline.set(time.Time{})
	s.writeDeadline.set(time.Time{})
}

// processFlags is used to update the state of the stream
// based on set flags, if any. Lock must be held
func (s *Stream) processFlags(flags uint16) {
	// Close the stream without holding the state lock
	closeStream := false
	defer func() {
		if closeStream {
			s.cleanup()
		}
	}()

	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	if flags&flagACK == flagACK {
		if s.state == streamSYNSent {
			s.state = streamEstablished
		}
		s.session.establishStream(s.id)
	}
	if flags&flagFIN == flagFIN {
		if s.readState == halfOpen {
			s.readState = halfClosed
			if s.writeState != halfOpen {
				// We're now fully closed.
				closeStream = true
				s.state = streamFinished
			}
			s.notifyWaiting()
		}
	}
	if flags&flagRST == flagRST {
		if s.readState == halfOpen {
			s.readState = halfReset
		}
		if s.writeState == halfOpen {
			s.writeState = halfReset
		}
		s.state = streamFinished
		closeStream = true
		s.notifyWaiting()
	}
}

// notifyWaiting notifies all the waiting channels
func (s *Stream) notifyWaiting() {
	asyncNotify(s.recvNotifyCh)
	asyncNotify(s.sendNotifyCh)
}

// incrSendWindow updates the size of our send window
func (s *Stream) incrSendWindow(hdr header, flags uint16) {
	s.processFlags(flags)
	// Increase window, unblock a sender
	atomic.AddUint32(&s.sendWindow, hdr.Length())
	asyncNotify(s.sendNotifyCh)
}

// readData is used to handle a data frame
func (s *Stream) readData(hdr header, flags uint16, conn io.Reader) error {
	s.processFlags(flags)

	// Check that our recv window is not exceeded
	length := hdr.Length()
	if length == 0 {
		return nil
	}

	// Copy into buffer
	if err := s.recvBuf.Append(conn, length); err != nil {
		s.session.logger.Printf("[ERR] yamux: Failed to read stream data on stream %d: %v", s.id, err)
		return err
	}
	// Unblock the reader
	asyncNotify(s.recvNotifyCh)
	return nil
}

// SetDeadline sets the read and write deadlines
func (s *Stream) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	if err := s.SetWriteDeadline(t); err != nil {
		return err
	}
	return nil
}

// SetReadDeadline sets the deadline for future Read calls.
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	if s.readState == halfOpen {
		s.readDeadline.set(t)
	}
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	if s.writeState == halfOpen {
		s.writeDeadline.set(t)
	}
	return nil
}
