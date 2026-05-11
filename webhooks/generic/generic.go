// Package generic provides a fall-through webhook provider that replicates
// the original HMAC-SHA256 signature behaviour (X-Webhook-Signature header
// or ?signature= query parameter). It should be used last so that
// more-specific providers take priority.
package generic

import (
	"net/http"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the generic catch-all webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "generic" }

// Match always returns true — this provider is a catch-all fallback.
func (p *provider) Match(_ *http.Request) bool { return true }

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		signature := r.Header.Get("X-Webhook-Signature")
		if signature == "" {
			signature = r.URL.Query().Get("signature")
		}

		if signature == "" || !webhooks.VerifyHexHMACSHA256(body, []byte(secret), signature) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return webhooks.NewEvent("generic", "", r, body), nil
}
