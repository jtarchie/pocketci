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
	"encoding/json"
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

	return webhooks.NewEvent("stripe", extractEventType(body), r, body), nil
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

	signed := timestamp + "." + string(body)
	for _, sig := range signatures {
		if webhooks.VerifyHexHMACSHA256([]byte(signed), []byte(secret), sig) {
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
