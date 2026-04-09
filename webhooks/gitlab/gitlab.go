// Package gitlab provides a webhook provider for GitLab events.
// It detects requests by the presence of the X-Gitlab-Event header and
// verifies signatures using X-Gitlab-Token (HMAC-SHA256, "sha256=<hex>" format).
package gitlab

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the GitLab webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "gitlab" }

// Match returns true when the request carries a X-Gitlab-Event header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("X-Gitlab-Event") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("X-Gitlab-Token")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !validateSignature(body, secret, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	eventType := r.Header.Get("X-Gitlab-Event")

	return buildEvent("gitlab", eventType, r, body), nil
}

// validateSignature checks the "sha256=<hex>" formatted GitLab signature.
func validateSignature(body []byte, secret, sigHeader string) bool {
	const prefix = "sha256="

	if strings.HasPrefix(sigHeader, prefix) {
		received := strings.TrimPrefix(sigHeader, prefix)

		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))

		return hmac.Equal([]byte(received), []byte(expected))
	}

	// GitLab also supports a plain token (not HMAC) — compare directly.
	return hmac.Equal([]byte(sigHeader), []byte(secret))
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
