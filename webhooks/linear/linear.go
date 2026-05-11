// Package linear provides a webhook provider for Linear events.
// It detects requests by the presence of the Linear-Signature header and
// verifies signatures using HMAC-SHA256 (plain hex, no prefix).
package linear

import (
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

		if !webhooks.VerifyHexHMACSHA256(body, []byte(secret), sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return webhooks.NewEvent("linear", extractEventType(body), r, body), nil
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
