// Package stripe provides a webhook provider for Stripe events.
// It detects requests by the presence of the Stripe-Signature header and
// verifies them using the Stripe signing protocol:
//
//	signed_payload = timestamp + "." + body
//	sig = hex(hmac-sha256(secret, signed_payload))
//
// The Stripe-Signature header format is: "t=<timestamp>,v1=<hex>[,v1=<hex>...]"
package stripe

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

// New returns the Stripe webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "stripe" }

// Match returns true when the request carries a Stripe-Signature header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("Stripe-Signature") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("Stripe-Signature")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !validateSignature(body, secret, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	eventType := extractEventType(body)

	return buildEvent("stripe", eventType, r, body), nil
}

// validateSignature verifies the Stripe "t=<ts>,v1=<hex>" signature header.
func validateSignature(body []byte, secret, sigHeader string) bool {
	var timestamp string

	var signatures []string

	for _, part := range strings.Split(sigHeader, ",") {
		switch {
		case strings.HasPrefix(part, "t="):
			timestamp = strings.TrimPrefix(part, "t=")
		case strings.HasPrefix(part, "v1="):
			signatures = append(signatures, strings.TrimPrefix(part, "v1="))
		}
	}

	if timestamp == "" || len(signatures) == 0 {
		return false
	}

	signed := fmt.Sprintf("%s.%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	expected := hex.EncodeToString(mac.Sum(nil))

	for _, sig := range signatures {
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return true
		}
	}

	return false
}

// extractEventType reads the top-level "type" field from the Stripe JSON payload.
func extractEventType(body []byte) string {
	var payload struct {
		Type string `json:"type"`
	}

	err := json.Unmarshal(body, &payload)
	if err != nil {
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
