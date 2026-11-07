package server

// P12 egress proof for the WSS framing contract: the transport adapter
// must keep the two Sink semantics distinct. Lifecycle frames take the
// bounded reliable queue; realtime snapshots take the depth-1 coalescing
// slot. Mapping SendUDP onto Reliable would still deliver snapshots (the
// e2e gate would stay green) while silently losing coalescing.

import "testing"

// recordingSink counts the Sink path each frame took.
type recordingSink struct {
	reliable int
	realtime int
	closed   bool
}

func (s *recordingSink) Reliable([]byte) error {
	s.reliable++
	return nil
}

func (s *recordingSink) Realtime([]byte) {
	s.realtime++
}

func (s *recordingSink) Close() error {
	s.closed = true
	return nil
}

func TestWSTransportKeepsReliableAndRealtimeDistinct(t *testing.T) {
	sink := &recordingSink{}
	tr := &wsTransport{sink: sink}

	if err := tr.SendTCP([]byte("lifecycle")); err != nil {
		t.Fatalf("SendTCP: %v", err)
	}
	if sink.reliable != 1 || sink.realtime != 0 {
		t.Fatalf("after SendTCP reliable=%d realtime=%d, want 1/0", sink.reliable, sink.realtime)
	}

	if err := tr.SendUDP([]byte("snapshot")); err != nil {
		t.Fatalf("SendUDP: %v", err)
	}
	if sink.reliable != 1 || sink.realtime != 1 {
		t.Fatalf("after SendUDP reliable=%d realtime=%d, want 1/1 (realtime must not queue)", sink.reliable, sink.realtime)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !sink.closed {
		t.Fatal("Close did not close the sink")
	}
}
