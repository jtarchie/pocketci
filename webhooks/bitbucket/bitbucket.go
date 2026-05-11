// Package bitbucket provides a webhook provider for Bitbucket events.
// It detects requests by the presence of the X-Event-Key header and
// verifies signatures using X-Hub-Signature (HMAC-SHA256, "sha256=<hex>" format).
package bitbucket

import (
	"net/http"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the Bitbucket webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "bitbucket" }

// Match returns true when the request carries a X-Event-Key header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("X-Event-Key") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("X-Hub-Signature")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !webhooks.VerifyHexHMACSHA256Prefixed(body, []byte(secret), sigHeader, "sha256=") {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return webhooks.NewEvent("bitbucket", r.Header.Get("X-Event-Key"), r, body), nil
}
