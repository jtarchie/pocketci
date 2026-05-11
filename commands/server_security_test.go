package commands

import (
	"strings"
	"testing"
)

// TestBuildAuthConfig_SessionSecretMinimumLength codifies PCI-SEC-AUTH-003:
// the session secret doubles as the HS256 JWT signing key, so any caller
// configuring OAuth must supply at least 32 bytes (256 bits, matching the
// HS256 hash output per RFC 7518). Short secrets are easily brute-forced
// offline from a captured JWT.
//
// The minimum is only enforced when OAuth providers are configured; basic
// auth deployments never sign JWTs, so they're exempt.
func TestBuildAuthConfig_SessionSecretMinimumLength(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		secret    string
		callback  string
		wantError string // substring; empty means must succeed
	}{
		{
			name:      "empty_secret_rejected",
			secret:    "",
			callback:  "https://ci.example.com",
			wantError: "CI_OAUTH_SESSION_SECRET is required",
		},
		{
			name:      "one_byte_rejected",
			secret:    "x",
			callback:  "https://ci.example.com",
			wantError: "at least 32 bytes",
		},
		{
			name:      "thirtyone_bytes_rejected",
			secret:    strings.Repeat("a", 31),
			callback:  "https://ci.example.com",
			wantError: "at least 32 bytes",
		},
		{
			name:      "thirtytwo_bytes_accepted",
			secret:    strings.Repeat("a", 32),
			callback:  "https://ci.example.com",
			wantError: "",
		},
		{
			name:      "longer_secret_accepted",
			secret:    strings.Repeat("a", 64),
			callback:  "https://ci.example.com",
			wantError: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := &Server{
				OAuthGithubClientID:     "id",
				OAuthGithubClientSecret: "shh",
				OAuthSessionSecret:      tc.secret,
				OAuthCallbackURL:        tc.callback,
			}

			_, _, _, err := s.buildAuthConfig()

			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantError, err)
			}
		})
	}
}

// TestBuildAuthConfig_BasicAuthIgnoresSessionSecret confirms the 32-byte
// floor does not apply to deployments that use only basic auth — they
// never mint JWTs, so a short or absent SessionSecret is fine there.
func TestBuildAuthConfig_BasicAuthIgnoresSessionSecret(t *testing.T) {
	t.Parallel()

	s := &Server{
		BasicAuth: "user:pw",
	}
	_, user, pw, err := s.buildAuthConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != "user" || pw != "pw" {
		t.Fatalf("unexpected basic auth: user=%q pw=%q", user, pw)
	}
}
