// Package slack provides a webhook provider for Slack Events API requests.
// It detects requests by the presence of the X-Slack-Signature header and
// verifies them using the Slack signing secret protocol:
//
//	base = "v0:" + X-Slack-Request-Timestamp + ":" + body
//	sig  = "v0=" + hex(hmac-sha256(signingSecret, base))
package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the Slack webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "slack" }

// Match returns true when the request carries a X-Slack-Signature header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("X-Slack-Signature") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		timestamp := r.Header.Get("X-Slack-Request-Timestamp")
		sigHeader := r.Header.Get("X-Slack-Signature")

		if timestamp == "" || sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !validateSignature(body, secret, timestamp, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	eventType := extractEventType(body)

	return buildEvent("slack", eventType, r, body), nil
}

// validateSignature verifies the Slack "v0=<hex>" signature.
func validateSignature(body []byte, secret, timestamp, sigHeader string) bool {
	const prefix = "v0="

	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}

	received := strings.TrimPrefix(sigHeader, prefix)

	base := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(received), []byte(expected))
}

// extractEventType reads the top-level "type" field from the Slack JSON payload.
// Returns an empty string if the body is not valid JSON or the field is absent.
func extractEventType(body []byte) string {
	var payload struct {
		Type string `json:"type"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
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
