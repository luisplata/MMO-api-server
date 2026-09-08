package server

// devAuthenticator tests (design: v1 dev-mode auth). Enabled accepts any
// credentials and returns the username as the account id; disabled
// rejects everything as a placeholder for real auth. Design D4: auth
// resolves only the account — the spawn position is NOT resolved here,
// it is resolved by SelectCharacter — so a successful Authenticate
// returns a nil spawn.

import (
	"testing"
)

func TestDevAuthenticator(t *testing.T) {
	cases := []struct {
		name     string
		auth     devAuthenticator
		username string
		password string
		wantID   string
		wantErr  bool
	}{
		{
			name:     "enabled accepts any credentials and returns account id only",
			auth:     devAuthenticator{enabled: true},
			username: "carol",
			password: "anything",
			wantID:   "carol",
		},
		{
			name:     "enabled rejects empty username",
			auth:     devAuthenticator{enabled: true},
			username: "",
			password: "pw",
			wantErr:  true,
		},
		{
			name:     "disabled rejects all",
			auth:     devAuthenticator{enabled: false},
			username: "carol",
			password: "pw",
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, spawn, err := tc.auth.Authenticate(tc.username, tc.password)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Authenticate(%q, %q) = (%q, %v, nil), want error", tc.username, tc.password, id, spawn)
				}
				return
			}
			if err != nil {
				t.Fatalf("Authenticate(%q, %q): %v, want success", tc.username, tc.password, err)
			}
			if id != tc.wantID {
				t.Errorf("account id = %q, want %q", id, tc.wantID)
			}
			if spawn != nil {
				t.Errorf("spawn = %v, want nil (auth resolves only the account, design D4)", spawn)
			}
		})
	}
}

// TestDevAuthenticatorReturnsOnlyAccountId triangulates the account-only
// contract: even when the authenticator is enabled and would previously
// have resolved a terrain-based spawn, Authenticate must yield the
// account id and a nil spawn — the spawn flows entirely through
// SelectCharacter in the selecting phase (design D4).
func TestDevAuthenticatorReturnsOnlyAccountId(t *testing.T) {
	d := devAuthenticator{enabled: true}
	id, spawn, err := d.Authenticate("dave", "pw")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id != "dave" {
		t.Errorf("account id = %q, want dave", id)
	}
	if spawn != nil {
		t.Errorf("spawn = %v, want nil (auth must not resolve a spawn)", spawn)
	}
}
