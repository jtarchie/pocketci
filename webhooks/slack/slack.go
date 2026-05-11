// Package slack provides a webhook provider for Slack Events API requests.
// It detects requests by the presence of the X-Slack-Signature header and
// verifies them using the Slack signing secret protocol:
//
//	base = "v0:" + X-Slack-Request-Timestamp + ":" + body
//	sig  = "v0=" + hex(hmac-sha256(signingSecret, base))
package slack

import (
	"encoding/json"
	"net/http"

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

		base := "v0:" + timestamp + ":" + string(body)
		if !webhooks.VerifyHexHMACSHA256Prefixed([]byte(base), []byte(secret), sigHeader, "v0=") {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return webhooks.NewEvent("slack", extractEventType(body), r, body), nil
}

// extractEventType reads the top-level "type" field from the Slack JSON payload.
// Returns an empty string if the body is not valid JSON or the field is absent.
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
