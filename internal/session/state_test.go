package session

// State machine tests (spec R11).
//
// Coverage: S11.1 illegal transitions are rejected — a session in one
// state can only reach the states the machine defines; S11.2 close is
// reachable from every live state (the absorbing Closed state).

import (
	"errors"
	"testing"
)

// TestStateTransitionTable drives the machine through every legal and
// illegal move. Each case asserts the returned state and whether the
// transition is allowed — the transition function is the sole source of
// truth the session layer consults before changing state.
func TestStateTransitionTable(t *testing.T) {
	cases := []struct {
		name    string
		from    State
		to      State
		legal   bool
		initial State // guard: an illegal move must leave the machine put
	}{
		// Legal forward progress along the defined path.
		{"connecting to handshaking", StateConnecting, StateHandshaking, true, StateConnecting},
		{"handshaking to authenticating", StateHandshaking, StateAuthenticating, true, StateHandshaking},
		{"authenticating to entering", StateAuthenticating, StateEntering, true, StateAuthenticating},
		{"entering to in-world", StateEntering, StateInWorld, true, StateEntering},
		// Close is legal from every live state (S11.2).
		{"connecting to closed", StateConnecting, StateClosed, true, StateConnecting},
		{"handshaking to closed", StateHandshaking, StateClosed, true, StateHandshaking},
		{"authenticating to closed", StateAuthenticating, StateClosed, true, StateAuthenticating},
		{"entering to closed", StateEntering, StateClosed, true, StateEntering},
		{"in-world to closed", StateInWorld, StateClosed, true, StateInWorld},
		// Illegal jumps: forward progress must pass through every
		// intermediate state (S11.1 example: authenticating → in-world
		// without EnterWorld is rejected).
		{"connecting to authenticating", StateConnecting, StateAuthenticating, false, StateConnecting},
		{"connecting to entering", StateConnecting, StateEntering, false, StateConnecting},
		{"connecting to in-world", StateConnecting, StateInWorld, false, StateConnecting},
		{"handshaking to entering", StateHandshaking, StateEntering, false, StateHandshaking},
		{"handshaking to in-world", StateHandshaking, StateInWorld, false, StateHandshaking},
		{"authenticating to in-world", StateAuthenticating, StateInWorld, false, StateAuthenticating},
		{"entering to handshaking", StateEntering, StateHandshaking, false, StateEntering},
		{"entering to authenticating", StateEntering, StateAuthenticating, false, StateEntering},
		{"in-world to authenticating", StateInWorld, StateAuthenticating, false, StateInWorld},
		// Backwards progress is never allowed.
		{"in-world to entering", StateInWorld, StateEntering, false, StateInWorld},
		{"handshaking to connecting", StateHandshaking, StateConnecting, false, StateHandshaking},
		// Closed is absorbing: nothing leaves it.
		{"closed to connecting", StateClosed, StateConnecting, false, StateClosed},
		{"closed to in-world", StateClosed, StateInWorld, false, StateClosed},
		{"closed to closed", StateClosed, StateClosed, false, StateClosed},
		// Steady state does not re-enter itself: a session already
		// in-world never "transitions" again.
		{"in-world to in-world", StateInWorld, StateInWorld, false, StateInWorld},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.from.Transition(tc.to)
			if tc.legal {
				if err != nil {
					t.Fatalf("Transition(%s -> %s) = %v, want nil", tc.from, tc.to, err)
				}
				if got != tc.to {
					t.Errorf("Transition(%s -> %s) returned %s, want %s", tc.from, tc.to, got, tc.to)
				}
			} else {
				if !errors.Is(err, ErrIllegalTransition) {
					t.Errorf("Transition(%s -> %s) err = %v, want ErrIllegalTransition", tc.from, tc.to, err)
				}
				if got != tc.initial {
					t.Errorf("illegal Transition(%s -> %s) returned state %s, must stay in %s", tc.from, tc.to, got, tc.initial)
				}
			}
			// CanTransitionTo must agree with Transition.
			if can := tc.from.CanTransitionTo(tc.to); can != tc.legal {
				t.Errorf("CanTransitionTo(%s -> %s) = %v, want %v (consistent with Transition)", tc.from, tc.to, can, tc.legal)
			}
		})
	}
}

// TestStateStringNames pins stable human-readable names for the six
// states — the names appear in errors and debug logs, so they must not
// silently change.
func TestStateStringNames(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{StateConnecting, "connecting"},
		{StateHandshaking, "handshaking"},
		{StateAuthenticating, "authenticating"},
		{StateEntering, "entering"},
		{StateInWorld, "in-world"},
		{StateClosed, "closed"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.state.String(); got != tc.want {
				t.Errorf("State(%d).String() = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestStateIsTerminal pins Closed as the only absorbing state.
func TestStateIsTerminal(t *testing.T) {
	for _, s := range []State{StateConnecting, StateHandshaking, StateAuthenticating, StateEntering, StateInWorld} {
		if s.IsTerminal() {
			t.Errorf("%s must not be terminal", s)
		}
	}
	if !StateClosed.IsTerminal() {
		t.Errorf("closed must be terminal")
	}
}
