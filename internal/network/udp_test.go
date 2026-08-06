package network

// UDP framing tests (spec R4). One datagram carries exactly one frame;
// a UDP frame must not exceed 1200 B (MTU-safe). Larger messages go over
// TCP (spec R9 — channel separation).
//
// Coverage: S4.1 MTU-safe datagram accepted, S4.2 oversized datagram
// rejected (never fragmented).

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestValidateUDPFrameSizeLimit covers S4.1/S4.2 at the pure-function
// level: frames ≤ 1200 B pass, anything larger is rejected.
func TestValidateUDPFrameSizeLimit(t *testing.T) {
	cases := []struct {
		name   string
		size   int
		accept bool
	}{
		{"empty-frame", 0, true},
		{"single-byte", 1, true},
		{"max-allowed-1200", MaxUDPFrameSize, true},
		{"one-over-1201", MaxUDPFrameSize + 1, false},
		{"well-over-1500-MTU", 1500, false},
		{"double-limit", 2 * MaxUDPFrameSize, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := bytes.Repeat([]byte{0xAB}, tc.size)
			err := ValidateUDPFrame(frame)
			if tc.accept && err != nil {
				t.Errorf("ValidateUDPFrame(%d B) = %v, want nil", tc.size, err)
			}
			if !tc.accept && !errors.Is(err, ErrDatagramTooLarge) {
				t.Errorf("ValidateUDPFrame(%d B) = %v, want ErrDatagramTooLarge", tc.size, err)
			}
		})
	}
}

// TestUDPDatagramRoundTrip covers S4.1 end-to-end over a loopback UDP
// socket: one datagram in, exactly one frame out, byte-identical.
func TestUDPDatagramRoundTrip(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer conn.Close()

	frame := bytes.Repeat([]byte{0xCD}, 1200) // MTU-safe max
	if err := SendDatagram(conn, conn.LocalAddr(), frame); err != nil {
		t.Fatalf("SendDatagram: %v", err)
	}

	// A socket read must return the exact frame as one datagram.
	got, addr, err := ReadDatagram(conn)
	if err != nil {
		t.Fatalf("ReadDatagram: %v", err)
	}
	if addr == nil {
		t.Fatal("ReadDatagram returned nil addr")
	}
	if !bytes.Equal(got, frame) {
		t.Errorf("datagram = %d bytes, want %d bytes identical", len(got), len(frame))
	}
}

// TestUDPOversizedDatagramRejected covers S4.2: sending a frame > 1200 B
// must fail with ErrDatagramTooLarge and must NOT write anything to the
// socket (never fragmented, never partially sent).
func TestUDPOversizedDatagramRejected(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer conn.Close()

	frame := bytes.Repeat([]byte{0xEF}, MaxUDPFrameSize+1)
	err = SendDatagram(conn, conn.LocalAddr(), frame)
	if !errors.Is(err, ErrDatagramTooLarge) {
		t.Fatalf("SendDatagram = %v, want ErrDatagramTooLarge", err)
	}

	// Nothing may arrive: the oversized frame was rejected before write.
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := ReadDatagram(conn); err == nil {
		t.Fatal("oversized datagram was partially written to the socket")
	}
}

// TestUDPReadRejectsOversizedDatagram: a peer that violates the limit and
// sends > 1200 B directly to the socket must be rejected on read too —
// the server never processes a frame above the MTU-safe bound.
func TestUDPReadRejectsOversizedDatagram(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer conn.Close()

	peer, err := net.DialUDP("udp", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer peer.Close()

	big := bytes.Repeat([]byte{0x01}, MaxUDPFrameSize+1)
	if _, err := peer.Write(big); err != nil {
		t.Fatalf("peer.Write: %v", err)
	}

	if _, _, err := ReadDatagram(conn); !errors.Is(err, ErrDatagramTooLarge) {
		t.Errorf("ReadDatagram = %v, want ErrDatagramTooLarge", err)
	}
}

// TestUDPFrameBoundarySizes exercises sizes around the 1200 B boundary
// via real sockets so the acceptance band is pinned end-to-end.
func TestUDPFrameBoundarySizes(t *testing.T) {
	for _, size := range []int{0, 1, 599, 1200} {
		t.Run("size-"+strconv.Itoa(size), func(t *testing.T) {
			conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatalf("ListenUDP: %v", err)
			}
			defer conn.Close()

			frame := bytes.Repeat([]byte{0x77}, size)
			if err := SendDatagram(conn, conn.LocalAddr(), frame); err != nil {
				t.Fatalf("SendDatagram: %v", err)
			}
			got, _, err := ReadDatagram(conn)
			if err != nil {
				t.Fatalf("ReadDatagram: %v", err)
			}
			if !bytes.Equal(got, frame) {
				t.Errorf("datagram = %d bytes, want %d", len(got), len(frame))
			}
		})
	}
}
