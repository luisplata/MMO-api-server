package server

// Recording outbound sink for the wiring tests: a production network.Sink
// whose reliable/realtime writers deliver into buffered channels, so tests
// observe exactly what a WSS client would receive without opening a socket.
// P13 removed the TCP/UDP transports, so there is no conn or datagram to
// inspect any more.

import (
	"testing"
	"time"

	"github.com/luisplata/mmo-api-server/internal/network"
)

type testSink struct {
	sink     network.Sink
	reliable chan []byte
	realtime chan []byte
	closed   chan struct{}
}

// newTestSink builds a bounded Sink plus the channels its writers feed.
// buffer is the channel depth; keep it >= the frame count a test reads
// before draining.
func newTestSink(buffer int) *testSink {
	ts := &testSink{
		reliable: make(chan []byte, buffer),
		realtime: make(chan []byte, buffer),
		closed:   make(chan struct{}),
	}
	ts.sink = network.NewSink(
		func(frame []byte) error { ts.reliable <- frame; return nil },
		func(frame []byte) error { ts.realtime <- frame; return nil },
		func() error { close(ts.closed); return nil },
	)
	return ts
}

// nextReliable blocks until the sink delivers its next reliable frame.
func (ts *testSink) nextReliable(t *testing.T) []byte {
	t.Helper()
	select {
	case frame := <-ts.reliable:
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a reliable frame")
		return nil
	}
}

// nextRealtime blocks until the sink delivers its next realtime frame.
func (ts *testSink) nextRealtime(t *testing.T) []byte {
	t.Helper()
	select {
	case frame := <-ts.realtime:
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a realtime frame")
		return nil
	}
}

// expectNoFrames asserts that neither channel produces a frame in a short
// window (a spurious async write would surface here).
func (ts *testSink) expectNoFrames(t *testing.T) {
	t.Helper()
	select {
	case frame := <-ts.reliable:
		t.Fatalf("unexpected reliable frame written: %v", frame)
	case frame := <-ts.realtime:
		t.Fatalf("unexpected realtime frame written: %v", frame)
	case <-time.After(100 * time.Millisecond):
	}
}

// waitClosed blocks until the sink's close callback runs.
func (ts *testSink) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-ts.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("sink was not closed")
	}
}
