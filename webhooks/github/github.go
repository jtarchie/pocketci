// Package github provides a webhook provider for GitHub events.
// It detects requests by the presence of the X-GitHub-Event header and
// verifies signatures using X-Hub-Signature-256 (HMAC-SHA256, "sha256=<hex>" format).
package github

import (
	"net/http"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the GitHub webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "github" }

// Match returns true when the request carries a X-GitHub-Event header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("X-GitHub-Event") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("X-Hub-Signature-256")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !webhooks.VerifyHexHMACSHA256Prefixed(body, []byte(secret), sigHeader, "sha256=") {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return webhooks.NewEvent("github", r.Header.Get("X-GitHub-Event"), r, body), nil
}
