package network

// Transport interface contract tests. The session state machine (PR3)
// will mock this interface; these tests pin the exact method set and
// prove a mock implementing it routes calls correctly.

import (
	"bytes"
	"errors"
	"testing"
)

// mockTransport records every call so tests can assert the interface
// routes SendTCP/SendUDP/Close to the right methods.
type mockTransport struct {
	tcpFrames [][]byte
	udpFrames [][]byte
	closed    bool
}

func (m *mockTransport) SendTCP(frame []byte) error {
	m.tcpFrames = append(m.tcpFrames, append([]byte(nil), frame...))
	return nil
}

func (m *mockTransport) SendUDP(frame []byte) error {
	m.udpFrames = append(m.udpFrames, append([]byte(nil), frame...))
	return nil
}

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

// compile-time assertion: mockTransport satisfies Transport.
var _ Transport = (*mockTransport)(nil)

// TestTransportRoutesCalls verifies the interface dispatches each method
// to the correct implementation and preserves frame bytes by value.
func TestTransportRoutesCalls(t *testing.T) {
	var tr Transport = &mockTransport{}
	tcp := []byte{0x4D, 0x4D, 1, 2, 3}
	udp := []byte{9, 8, 7}

	if err := tr.SendTCP(tcp); err != nil {
		t.Fatalf("SendTCP: %v", err)
	}
	if err := tr.SendUDP(udp); err != nil {
		t.Fatalf("SendUDP: %v", err)
	}

	m := tr.(*mockTransport)
	if len(m.tcpFrames) != 1 || !bytes.Equal(m.tcpFrames[0], tcp) {
		t.Errorf("TCP frames = %x, want [%x]", m.tcpFrames, tcp)
	}
	if len(m.udpFrames) != 1 || !bytes.Equal(m.udpFrames[0], udp) {
		t.Errorf("UDP frames = %x, want [%x]", m.udpFrames, udp)
	}
	if m.closed {
		t.Errorf("Close must not be called implicitly")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !m.closed {
		t.Errorf("Close did not reach the implementation")
	}
}

// TestTransportErrorsPropagate: an implementation that fails must surface
// its error through the interface (no swallowing).
func TestTransportErrorsPropagate(t *testing.T) {
	wantErr := errors.New("boom")
	tr := Transport(&failingTransport{err: wantErr})
	if err := tr.SendTCP(nil); !errors.Is(err, wantErr) {
		t.Errorf("SendTCP err = %v, want %v", err, wantErr)
	}
	if err := tr.SendUDP(nil); !errors.Is(err, wantErr) {
		t.Errorf("SendUDP err = %v, want %v", err, wantErr)
	}
	if err := tr.Close(); !errors.Is(err, wantErr) {
		t.Errorf("Close err = %v, want %v", err, wantErr)
	}
}

type failingTransport struct {
	err error
}

func (f *failingTransport) SendTCP([]byte) error { return f.err }
func (f *failingTransport) SendUDP([]byte) error { return f.err }
func (f *failingTransport) Close() error         { return f.err }
