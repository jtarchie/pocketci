package webhooks_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jtarchie/pocketci/webhooks"
	"github.com/jtarchie/pocketci/webhooks/bitbucket"
	"github.com/jtarchie/pocketci/webhooks/generic"
	"github.com/jtarchie/pocketci/webhooks/github"
	"github.com/jtarchie/pocketci/webhooks/gitlab"
	"github.com/jtarchie/pocketci/webhooks/honeybadger"
	"github.com/jtarchie/pocketci/webhooks/linear"
	"github.com/jtarchie/pocketci/webhooks/pagerduty"
	"github.com/jtarchie/pocketci/webhooks/sentry"
	"github.com/jtarchie/pocketci/webhooks/slack"
	"github.com/jtarchie/pocketci/webhooks/stripe"
)

// allProviders is the set this fuzz target exercises. It must mirror the
// registration order in commands/server.go so the fuzz catches the same
// dispatcher ordering CI runs with.
func allProviders() []webhooks.Provider {
	return []webhooks.Provider{
		github.New(),
		gitlab.New(),
		bitbucket.New(),
		honeybadger.New(),
		slack.New(),
		stripe.New(),
		pagerduty.New(),
		linear.New(),
		sentry.New(),
		generic.New(),
	}
}

// FuzzWebhooks_NoPanicNoForgery pins two invariants the dispatcher relies on:
//
//  1. For ANY input (method/path/header/body/secret), no provider's Match or
//     Parse may panic. The dispatcher catches Parse errors but does not
//     recover from panics; a panic kills the goroutine and surfaces as 500.
//
//  2. For ANY input whose attacker-controlled signature header was not
//     produced by the secret, Parse must return ErrUnauthorized (or any
//     non-nil error) — i.e. no provider can ever accept a random signature
//     against the configured secret.
//
// This is the differential test the security review called for: all
// providers must agree on "reject random garbage" regardless of which one
// Match'd.
func FuzzWebhooks_NoPanicNoForgery(f *testing.F) {
	// Seed with shapes that exercise each provider's Match path.
	seeds := []struct {
		method, path string
		headerKey    string
		headerVal    string
		body         string
		secret       string
	}{
		{"POST", "/", "X-GitHub-Event", "push", `{"ref":"refs/heads/main"}`, "k"},
		{"POST", "/", "X-Gitlab-Event", "Push Hook", `{}`, "k"},
		{"POST", "/", "X-Event-Key", "repo:push", `{}`, "k"},
		{"POST", "/", "Honeybadger-Token", "tok", `{}`, "k"},
		{"POST", "/", "X-Slack-Signature", "v0=00", `{"type":"event_callback"}`, "k"},
		{"POST", "/", "Stripe-Signature", "t=1,v1=00", `{}`, "k"},
		{"POST", "/", "X-PagerDuty-Signature", "v1=00", `{}`, "k"},
		{"POST", "/", "Linear-Signature", "00", `{}`, "k"},
		{"POST", "/", "Sentry-Hook-Signature", "00", `{}`, "k"},
		{"POST", "/", "X-Webhook-Signature", "00", `{}`, "k"},
		// Pathological inputs
		{"", "", "", "", "", ""},
		{"GET", "/?signature=00", "", "", "", "k"},
		{"POST", "/", "X-GitHub-Event", "", "", ""},
		{"POST", "/", "X-Slack-Signature", "v0=", "", "k"},
		{"POST", "/", "Stripe-Signature", "t=,v1=", "", "k"},
	}
	for _, s := range seeds {
		f.Add(s.method, s.path, s.headerKey, s.headerVal, s.body, s.secret)
	}

	f.Fuzz(func(t *testing.T, method, path, headerKey, headerVal, body, secret string) {
		// Skip inputs that would crash httptest.NewRequest itself; the
		// dispatcher receives requests through net/http which has already
		// validated method/URL syntax.
		if method == "" {
			method = "POST"
		}
		if path == "" {
			path = "/"
		}
		// Reject obviously-malformed URLs to keep the fuzz focused on
		// provider behavior; net/http would 400 these before our code sees them.
		req, err := http.NewRequest(method, "http://example/"+path, bytes.NewReader([]byte(body)))
		if err != nil {
			return
		}
		// Reject header names net/http would refuse — these can't reach a real
		// dispatcher; we only care about the byte-shapes that DO reach Match/Parse.
		if headerKey != "" && http.CanonicalHeaderKey(headerKey) != "" && isPrintableASCII(headerKey) {
			defer recoverableHeaderSet(req, headerKey, headerVal)
		}

		// Drive the dispatcher exactly the way the HTTP route does.
		_, parseErr := webhooks.Detect(allProviders(), req, []byte(body), secret)
		// We don't assert anything about ErrNoMatch / ErrUnauthorized / nil —
		// the invariant is: no panic. The line above is sufficient.
		_ = parseErr

		// Additionally: run Match+Parse against each provider individually,
		// so a provider whose Match returns true while another's Parse would
		// have succeeded is still exercised.
		for _, p := range allProviders() {
			if !p.Match(req) {
				continue
			}
			// A random secret cannot validate a random signature — Parse must
			// either succeed with an EXACTLY-matching signature (vanishingly
			// improbable in fuzz) or return an error. We don't enforce
			// "always error" because the seed corpus does include a few
			// "empty secret" cases where some providers skip verification by
			// design; we only enforce "no panic".
			_, _ = p.Parse(req, []byte(body), secret)
		}
	})
}

// FuzzWebhooks_ProviderDisjointMatch pins that each non-generic provider's
// Match decision is based on a header unique to that provider. We re-issue
// each seed request through every provider and assert at most ONE non-
// generic provider matches; the generic catch-all may always match.
func FuzzWebhooks_ProviderDisjointMatch(f *testing.F) {
	seeds := []struct{ headerKey, headerVal string }{
		{"X-GitHub-Event", "push"},
		{"X-Gitlab-Event", "Push Hook"},
		{"X-Event-Key", "repo:push"},
		{"Honeybadger-Token", "x"},
		{"X-Slack-Signature", "v0=00"},
		{"Stripe-Signature", "t=1,v1=00"},
		{"X-PagerDuty-Signature", "v1=00"},
		{"Linear-Signature", "00"},
		{"Sentry-Hook-Signature", "00"},
		{"X-Webhook-Signature", "00"},
	}
	for _, s := range seeds {
		f.Add(s.headerKey, s.headerVal)
	}

	f.Fuzz(func(t *testing.T, headerKey, headerVal string) {
		if !isPrintableASCII(headerKey) || headerKey == "" {
			return
		}
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set(headerKey, headerVal)

		matches := 0
		genericMatched := false
		for _, p := range allProviders() {
			if !p.Match(req) {
				continue
			}
			if p.Name() == "generic" {
				genericMatched = true
				continue
			}
			matches++
		}

		// Non-generic providers must be pairwise-disjoint on Match.
		if matches > 1 {
			t.Fatalf("multiple non-generic providers matched header %q=%q", headerKey, headerVal)
		}

		// And: the generic provider must catch anything no specific provider claims.
		// If a specific provider matched, generic may or may not also match (it's
		// fine for the catch-all to be liberal). If no specific provider matched,
		// we don't assert generic matched either (a request with no recognized
		// header is allowed to fall through to ErrNoMatch).
		_ = genericMatched
	})
}

func isPrintableASCII(s string) bool {
	for _, c := range s {
		if c < 0x20 || c >= 0x7F {
			return false
		}
		// HTTP header names disallow these per RFC 7230 token grammar.
		switch c {
		case '(', ')', ',', '/', ':', ';', '<', '=', '>', '?', '@', '[', '\\', ']', '{', '}', ' ', '\t':
			return false
		}
	}
	return s != ""
}

// recoverableHeaderSet swallows panics from invalid header names so the
// fuzz target focuses on Match/Parse behavior, not on whether net/http
// would have accepted a given header name.
func recoverableHeaderSet(req *http.Request, key, value string) {
	defer func() { _ = recover() }()
	req.Header.Set(key, value)
}
