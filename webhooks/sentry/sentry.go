// Package sentry provides a webhook provider for Sentry events.
// It detects requests by the presence of the Sentry-Hook-Signature header and
// verifies signatures using HMAC-SHA256 (plain hex, no prefix).
package sentry

import (
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

		if !webhooks.VerifyHexHMACSHA256(body, []byte(secret), sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	eventType := r.Header.Get("Sentry-Hook-Resource")
	if eventType == "" {
		eventType = extractEventType(body)
	}

	return webhooks.NewEvent("sentry", eventType, r, body), nil
}

// extractEventType reads the top-level "action" or "type" field from the Sentry JSON payload.
func extractEventType(body []byte) string {
	var payload struct {
		Action string `json:"action"`
		Type   string `json:"type"`
	}

	err := json.Unmarshal(body, &payload)
	if err != nil {
		return ""
	}

	if payload.Action != "" {
		return payload.Action
	}

	return payload.Type
}
