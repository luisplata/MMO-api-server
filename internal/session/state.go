// Package session implements the connection lifecycle (spec R10–R13):
// the state machine, the seven-step handshake, authentication, and UDP
// token binding, all driven over the network.Transport seam so tests
// mock the transport instead of spinning sockets.
package session

import "errors"

// State is a session's lifecycle phase (spec R11). A session progresses
// through the machine in order:
//
//	connecting → handshaking → authenticating → entering → in-world
//
// and may jump to closed from ANY live state (S11.2); closed is
// absorbing.
type State uint8

const (
	// StateConnecting is a fresh TCP connection before any message.
	StateConnecting State = iota
	// StateHandshaking follows a Hello accepted for the supported
	// version range; ServerInfo has been sent.
	StateHandshaking
	// StateAuthenticating follows an AuthRequest being processed.
	StateAuthenticating
	// StateEntering follows a successful AuthResponse; the server waits
	// for the client's EnterWorld before entering the world.
	StateEntering
	// StateInWorld is steady state: the client has received its
	// WorldSnapshot and the UDP binding may be established.
	StateInWorld
	// StateClosed is absorbing; the transport is torn down and any UDP
	// binding released.
	StateClosed
)

// stateNames maps a State to its stable human-readable name.
var stateNames = [...]string{
	StateConnecting:     "connecting",
	StateHandshaking:    "handshaking",
	StateAuthenticating: "authenticating",
	StateEntering:       "entering",
	StateInWorld:        "in-world",
	StateClosed:         "closed",
}

// String returns the stable state name (used in errors and logs).
func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "unknown"
}

// ErrIllegalTransition is returned when a session would move between
// states the machine does not define (spec S11.1).
var ErrIllegalTransition = errors.New("session: illegal state transition")

// legalTransitions is the transition table. A live state may move to its
// successor on the defined path or straight to closed; closed is
// absorbing and no state may transition to itself (steady state does not
// re-enter — it simply does not transition).
var legalTransitions = map[State]map[State]bool{
	StateConnecting:     {StateHandshaking: true, StateClosed: true},
	StateHandshaking:    {StateAuthenticating: true, StateClosed: true},
	StateAuthenticating: {StateEntering: true, StateClosed: true},
	StateEntering:       {StateInWorld: true, StateClosed: true},
	StateInWorld:        {StateClosed: true},
	StateClosed:         {},
}

// CanTransitionTo reports whether the machine allows moving from s to
// dst (spec S11.1).
func (s State) CanTransitionTo(dst State) bool {
	return legalTransitions[s][dst]
}

// Transition moves the machine from s to dst, returning the destination
// on success or ErrIllegalTransition (leaving s unchanged) when the move
// is not defined by the machine.
func (s State) Transition(dst State) (State, error) {
	if !s.CanTransitionTo(dst) {
		return s, ErrIllegalTransition
	}
	return dst, nil
}

// IsTerminal reports whether the state is absorbing (closed).
func (s State) IsTerminal() bool {
	return s == StateClosed
}
