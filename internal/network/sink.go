package network

// Outbound transport seam (design P2): a per-connection Sink absorbs
// backpressure so no peer can stall the simulation. Reliable frames
// queue FIFO up to a bound (full → close); Realtime frames coalesce to
// the latest (depth-1 last-wins). One writer goroutine drains both.

import (
	"errors"
	"sync"
)

// ErrSinkFull reports a full reliable queue; the connection closes.
var ErrSinkFull = errors.New("network: sink reliable queue full")

// Sink is the outbound counterpart to FrameSource: all outbound traffic
// flows through it, keeping the transport swappable with identical
// queueing semantics.
type Sink interface {
	// Reliable enqueues a frame in order; a full queue closes the
	// connection and returns ErrSinkFull.
	Reliable(frame []byte) error
	// Realtime stores frame as the latest (non-blocking, last-wins).
	Realtime(frame []byte)
	// Close stops the writer and tears down the connection. Idempotent.
	Close() error
}

// reliableQueueCap is the bounded depth of the reliable queue (design P2).
const reliableQueueCap = 32

// NewSink builds a bounded Sink. writeReliable/writeRealtime emit one
// reliable/realtime frame; closeConn tears down the connection.
func NewSink(
	writeReliable, writeRealtime func(frame []byte) error,
	closeConn func() error,
) Sink {
	s := &connSink{
		writeReliable: writeReliable,
		writeRealtime: writeRealtime,
		closeConn:     closeConn,
		reliable:      make(chan []byte, reliableQueueCap),
		rtWake:        make(chan struct{}, 1),
		done:          make(chan struct{}),
		loopDone:      make(chan struct{}),
	}
	go s.loop()
	return s
}

type connSink struct {
	writeReliable func(frame []byte) error
	writeRealtime func(frame []byte) error
	closeConn     func() error

	reliable chan []byte // bounded reliable queue

	mu      sync.Mutex
	rtFrame []byte        // latest realtime frame (depth-1, last-wins)
	rtWake  chan struct{} // buffered(1) signal to the writer

	done      chan struct{}
	loopDone  chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (s *connSink) Reliable(frame []byte) error {
	select {
	case <-s.done:
		return ErrSinkFull // closed: no more frames accepted
	default:
	}
	select {
	case s.reliable <- frame:
		return nil
	default:
		s.closeNow()
		return ErrSinkFull
	}
}

func (s *connSink) Realtime(frame []byte) {
	if frame == nil {
		return
	}
	s.mu.Lock()
	s.rtFrame = frame
	s.mu.Unlock()
	select {
	case s.rtWake <- struct{}{}:
	default:
	}
}

// Close stops accepting frames, waits for the writer to stop, then tears
// down the connection. Idempotent.
func (s *connSink) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		<-s.loopDone
		s.closeErr = s.closeConn()
	})
	return s.closeErr
}

// closeNow tears down immediately: the backpressure and write-failure
// paths have nothing worth flushing.
func (s *connSink) closeNow() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.closeErr = s.closeConn()
	})
}

// takeRealtime drains the realtime slot, returning the latest frame.
func (s *connSink) takeRealtime() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	frame := s.rtFrame
	s.rtFrame = nil
	return frame
}

// loop drains the reliable queue and realtime slot until closed or a
// write fails (which closes the connection).
func (s *connSink) loop() {
	defer close(s.loopDone)
	for {
		select {
		case <-s.done:
			return
		case frame := <-s.reliable:
			if err := s.writeReliable(frame); err != nil {
				s.closeNow()
				return
			}
		case <-s.rtWake:
			frame := s.takeRealtime()
			if frame == nil {
				continue
			}
			if err := s.writeRealtime(frame); err != nil {
				s.closeNow()
				return
			}
		}
	}
}
