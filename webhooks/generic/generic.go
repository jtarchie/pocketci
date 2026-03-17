// Package generic provides a fall-through webhook provider that replicates
// the original HMAC-SHA256 signature behaviour (X-Webhook-Signature header
// or ?signature= query parameter). It should be used last so that
// more-specific providers take priority.
package generic

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

		if signature == "" || !validateSignature(body, secret, signature) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return buildEvent("generic", "", r, body), nil
}

func validateSignature(body []byte, secret, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expected))
}

func buildEvent(provider, eventType string, r *http.Request, body []byte) *webhooks.Event {
	headers := make(map[string]string)
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	query := make(map[string]string)
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}

	return &webhooks.Event{
		Provider:  provider,
		EventType: eventType,
		Method:    r.Method,
		URL:       r.URL.String(),
		Headers:   headers,
		Body:      string(body),
		Query:     query,
	}
}
