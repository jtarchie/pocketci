// Package linear provides a webhook provider for Linear events.
// It detects requests by the presence of the Linear-Signature header and
// verifies signatures using HMAC-SHA256 (plain hex, no prefix).
package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the Linear webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "linear" }

// Match returns true when the request carries a Linear-Signature header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("Linear-Signature") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("Linear-Signature")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !validateSignature(body, secret, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	eventType := extractEventType(body)

	return buildEvent("linear", eventType, r, body), nil
}

// validateSignature checks the plain hex HMAC-SHA256 Linear signature.
func validateSignature(body []byte, secret, sigHeader string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sigHeader), []byte(expected))
}

// extractEventType reads the top-level "type" or "action" field from the Linear JSON payload.
func extractEventType(body []byte) string {
	var payload struct {
		Type   string `json:"type"`
		Action string `json:"action"`
	}

	err := json.Unmarshal(body, &payload)
	if err != nil {
		return ""
	}

	if payload.Type != "" {
		return payload.Type
	}

	return payload.Action
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
