// Package pagerduty provides a webhook provider for PagerDuty events.
// It detects requests by the presence of the X-PagerDuty-Signature header and
// verifies them using HMAC-SHA256. The header contains one or more comma-separated
// signatures in the format "v1=<hex>". A request is valid if any signature matches.
package pagerduty

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the PagerDuty webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "pagerduty" }

// Match returns true when the request carries a X-PagerDuty-Signature header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("X-PagerDuty-Signature") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("X-PagerDuty-Signature")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !validateSignature(body, secret, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return webhooks.NewEvent("pagerduty", extractEventType(body), r, body), nil
}

// validateSignature checks comma-separated "v1=<hex>" PagerDuty signatures.
// A request is valid if any of the provided signatures match.
func validateSignature(body []byte, secret, sigHeader string) bool {
	for _, part := range strings.Split(sigHeader, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "v1=") {
			continue
		}

		if webhooks.VerifyHexHMACSHA256(body, []byte(secret), strings.TrimPrefix(part, "v1=")) {
			return true
		}
	}

	return false
}

// extractEventType reads the event type from a PagerDuty JSON payload.
// PagerDuty wraps events in a "messages" array; each message has an "event" field.
// Falls back to the top-level "event" or "type" field for simpler payloads.
func extractEventType(body []byte) string {
	var payload struct {
		Messages []struct {
			Event string `json:"event"`
		} `json:"messages"`
		Event string `json:"event"`
		Type  string `json:"type"`
	}

	err := json.Unmarshal(body, &payload)
	if err != nil {
		return ""
	}

	if len(payload.Messages) > 0 && payload.Messages[0].Event != "" {
		return payload.Messages[0].Event
	}

	if payload.Event != "" {
		return payload.Event
	}

	return payload.Type
}
