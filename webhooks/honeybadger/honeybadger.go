// Package honeybadger provides a webhook provider for Honeybadger webhooks.
// It detects requests by the Honeybadger-Token header and validates the token
// against the configured webhook secret.
package honeybadger

import (
	"crypto/hmac"
	"encoding/json"
	"net/http"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the Honeybadger webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "honeybadger" }

// Match returns true when the request carries a Honeybadger-Token header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("Honeybadger-Token") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret == "" {
		return nil, webhooks.ErrUnauthorized
	}

	token := r.Header.Get("Honeybadger-Token")
	if token == "" {
		return nil, webhooks.ErrUnauthorized
	}

	if !hmac.Equal([]byte(token), []byte(secret)) {
		return nil, webhooks.ErrUnauthorized
	}

	eventType := extractEventType(body)

	return buildEvent("honeybadger", eventType, r, body), nil
}

// extractEventType reads a top-level "type" or "event" from the payload.
// Returns an empty string when the body is not valid JSON or no field exists.
func extractEventType(body []byte) string {
	var payload map[string]any
	err := json.Unmarshal(body, &payload)
	if err != nil {
		return ""
	}

	if value, ok := payload["type"].(string); ok {
		return value
	}

	if value, ok := payload["event"].(string); ok {
		return value
	}

	return ""
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
