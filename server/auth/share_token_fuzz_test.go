package auth_test

import (
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/server/auth"
)

// FuzzValidateShareToken — no input should panic or hang; valid round-trips
// must succeed; mutations of any produced token must fail.
func FuzzValidateShareToken(f *testing.F) {
	seeds := []struct{ token, secret string }{
		{"", ""},
		{".", ""},
		{"..", ""},
		{"a.b.c", "k"},
		{"run.1.deadbeef", "k"},
		{"run.+1.deadbeef", "k"},                     // leading-+ ParseInt accepts (today)
		{"run.999999999999999999.deadbeef", "k"},     // near-overflow expiry
		{"run.18446744073709551616.deadbeef", "k"},   // overflow ParseInt(int64)
		{"run.-1.deadbeef", "k"},                     // negative expiry
		{"run.1.nothex", "k"},
		{"\x00.\x00.\x00", "k"},
		{strings.Repeat("a", 4096), "k"},
		{"run.1.AA", strings.Repeat("k", 1024)},
	}
	for _, s := range seeds {
		f.Add(s.token, s.secret)
	}

	f.Fuzz(func(t *testing.T, token, secret string) {
		_, _ = auth.ValidateShareToken(token, secret)
	})
}

// FuzzShareTokenRoundTrip — Generate(runID,secret) followed by Validate
// against the same secret returns the same runID; against a different secret
// always errors; against the same secret with any one byte flipped errors.
func FuzzShareTokenRoundTrip(f *testing.F) {
	seeds := []struct{ runID, secret string }{
		{"run-a", "deployment-secret"},
		{"00000000-0000-0000-0000-000000000000", "k"},
		{"a", "b"},
		{"αβγ", "δε"},
		{strings.Repeat("R", 256), strings.Repeat("S", 256)},
	}
	for _, s := range seeds {
		f.Add(s.runID, s.secret)
	}

	f.Fuzz(func(t *testing.T, runID, secret string) {
		if runID == "" {
			return // GenerateShareToken refuses empty runID by contract
		}

		token, err := auth.GenerateShareToken(runID, secret)
		if err != nil {
			t.Fatalf("Generate(%q, …) failed: %v", runID, err)
		}

		claims, err := auth.ValidateShareToken(token, secret)
		if err != nil {
			t.Fatalf("round-trip Validate(%q, same secret) failed: %v", token, err)
		}
		if claims.RunID != runID {
			t.Fatalf("Validate returned RunID %q, want %q", claims.RunID, runID)
		}

		// Wrong secret must always fail. Pick a secret guaranteed different.
		alt := secret + "x"
		if _, err := auth.ValidateShareToken(token, alt); err == nil {
			t.Fatalf("Validate accepted token under wrong secret")
		}

		// Flipping the last sig byte must invalidate the token.
		if last := len(token) - 1; last > 0 {
			flipped := []byte(token)
			flipped[last] ^= 0x01
			if _, err := auth.ValidateShareToken(string(flipped), secret); err == nil {
				t.Fatalf("Validate accepted token with mutated last byte")
			}
		}
	})
}
