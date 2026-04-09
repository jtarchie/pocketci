// Package bitbucket provides a webhook provider for Bitbucket events.
// It detects requests by the presence of the X-Event-Key header and
// verifies signatures using X-Hub-Signature (HMAC-SHA256, "sha256=<hex>" format).
package bitbucket

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

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

		if !validateSignature(body, secret, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	eventType := r.Header.Get("X-Event-Key")

	return buildEvent("bitbucket", eventType, r, body), nil
}

// validateSignature checks the "sha256=<hex>" formatted Bitbucket signature.
func validateSignature(body []byte, secret, sigHeader string) bool {
	const prefix = "sha256="

	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}

	received := strings.TrimPrefix(sigHeader, prefix)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(received), []byte(expected))
}

func buildEvent(providerName, eventType string, r *http.Request, body []byte) *webhooks.Event {
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
		Provider:  providerName,
		EventType: eventType,
		Method:    r.Method,
		URL:       r.URL.String(),
		Headers:   headers,
		Body:      string(body),
		Query:     query,
	}
}
