package network

// Outbound Sink tests (design P2): the bounded reliable queue and the
// depth-1 last-wins realtime slot, with injected writers so the queueing
// semantics are exercised without sockets.

import (
	"errors"
	"testing"
	"time"
)

// TestConnSinkRealtimeDelivers: a single realtime frame is written through.
func TestConnSinkRealtimeDelivers(t *testing.T) {
	wrote := make(chan []byte, 1)
	s := NewSink(
		func(frame []byte) error { return nil },
		func(frame []byte) error { wrote <- append([]byte(nil), frame...); return nil },
		func() error { return nil },
	)
	defer s.Close()

	s.Realtime([]byte{7})
	select {
	case got := <-wrote:
		if len(got) != 1 || got[0] != 7 {
			t.Fatalf("realtime write = %v, want [7]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the realtime frame")
	}
}

// TestConnSinkRealtimeCoalesces: a burst of 10 realtime frames against a
// pinned writer coalesces to the latest (depth-1 last-wins).
func TestConnSinkRealtimeCoalesces(t *testing.T) {
	wrote := make(chan []byte, reliableQueueCap)
	release := make(chan struct{})
	writeRealtime := func(frame []byte) error {
		wrote <- append([]byte(nil), frame...)
		<-release // pin the writer mid-write (a slow peer)
		return nil
	}
	s := NewSink(
		func(frame []byte) error { return nil },
		writeRealtime,
		func() error { return nil },
	)
	defer s.Close()

	s.Realtime([]byte{1})
	select {
	case got := <-wrote:
		if len(got) != 1 || got[0] != 1 {
			t.Fatalf("first write = %v, want [1]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first realtime write")
	}

	for i := byte(2); i <= 10; i++ { // burst coalesces into the slot
		s.Realtime([]byte{i})
	}
	close(release)

	select {
	case got := <-wrote:
		if len(got) != 1 || got[0] != 10 {
			t.Fatalf("coalesced write = %v, want [10] (frames 2..9 dropped)", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the coalesced realtime frame")
	}
}

// TestConnSinkReliableFIFO: reliable frames are written in enqueue order.
func TestConnSinkReliableFIFO(t *testing.T) {
	wrote := make(chan []byte, reliableQueueCap)
	s := NewSink(
		func(frame []byte) error { wrote <- append([]byte(nil), frame...); return nil },
		func(frame []byte) error { return nil },
		func() error { return nil },
	)
	defer s.Close()

	for i := byte(0); i < 3; i++ {
		if err := s.Reliable([]byte{i}); err != nil {
			t.Fatalf("Reliable(%d): %v", i, err)
		}
	}
	for i := byte(0); i < 3; i++ {
		select {
		case got := <-wrote:
			if len(got) != 1 || got[0] != i {
				t.Fatalf("frame %d = %v, want [%d]", i, got, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for reliable frame %d", i)
		}
	}
}

// TestConnSinkReliableFullCloses: a full reliable queue (32) closes the
// connection and reports ErrSinkFull.
func TestConnSinkReliableFullCloses(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	closed := make(chan struct{})
	s := NewSink(
		func(frame []byte) error {
			select { // signal once; later drains must not panic
			case started <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
		func(frame []byte) error { return nil },
		func() error { close(closed); return nil },
	)
	defer s.Close()

	if err := s.Reliable([]byte{0}); err != nil {
		t.Fatalf("Reliable(0): %v", err)
	}
	<-started // writer pinned on the first frame; queue now empty

	for i := 1; i <= reliableQueueCap; i++ { // fill to the bound (32)
		if err := s.Reliable([]byte{byte(i)}); err != nil {
			t.Fatalf("Reliable(%d) = %v, want nil within capacity", i, err)
		}
	}
	if err := s.Reliable([]byte{0xFF}); !errors.Is(err, ErrSinkFull) {
		t.Fatalf("Reliable beyond capacity = %v, want ErrSinkFull", err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("connection not closed on full reliable queue")
	}
	close(release) // release the pinned writer so its goroutine can exit
}
