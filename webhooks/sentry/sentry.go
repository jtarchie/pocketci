// Package sentry provides a webhook provider for Sentry events.
// It detects requests by the presence of the Sentry-Hook-Signature header and
// verifies signatures using HMAC-SHA256 (plain hex, no prefix).
package sentry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the Sentry webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "sentry" }

// Match returns true when the request carries a Sentry-Hook-Signature header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("Sentry-Hook-Signature") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("Sentry-Hook-Signature")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !validateSignature(body, secret, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	eventType := r.Header.Get("Sentry-Hook-Resource")
	if eventType == "" {
		eventType = extractEventType(body)
	}

	return buildEvent("sentry", eventType, r, body), nil
}

// validateSignature checks the plain hex HMAC-SHA256 Sentry signature.
func validateSignature(body []byte, secret, sigHeader string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sigHeader), []byte(expected))
}

// extractEventType reads the top-level "action" or "type" field from the Sentry JSON payload.
func extractEventType(body []byte) string {
	var payload struct {
		Action string `json:"action"`
		Type   string `json:"type"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	if payload.Action != "" {
		return payload.Action
	}

	return payload.Type
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
