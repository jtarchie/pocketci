// Package gitlab provides a webhook provider for GitLab events.
// It detects requests by the presence of the X-Gitlab-Event header and
// verifies signatures using X-Gitlab-Token. The token may be either a plain
// shared secret or an HMAC-SHA256 signature in "sha256=<hex>" format.
package gitlab

import (
	"crypto/hmac"
	"net/http"
	"strings"

	"github.com/jtarchie/pocketci/webhooks"
)

type provider struct{}

// New returns the GitLab webhook provider.
func New() webhooks.Provider { return &provider{} }

func (p *provider) Name() string { return "gitlab" }

// Match returns true when the request carries a X-Gitlab-Event header.
func (p *provider) Match(r *http.Request) bool {
	return r.Header.Get("X-Gitlab-Event") != ""
}

func (p *provider) Parse(r *http.Request, body []byte, secret string) (*webhooks.Event, error) {
	if secret != "" {
		sigHeader := r.Header.Get("X-Gitlab-Token")
		if sigHeader == "" {
			return nil, webhooks.ErrUnauthorized
		}

		if !validateSignature(body, secret, sigHeader) {
			return nil, webhooks.ErrUnauthorized
		}
	}

	return webhooks.NewEvent("gitlab", r.Header.Get("X-Gitlab-Event"), r, body), nil
}

// validateSignature accepts either the HMAC-SHA256 "sha256=<hex>" form or a
// plain shared-secret token compared constant-time.
func validateSignature(body []byte, secret, sigHeader string) bool {
	if strings.HasPrefix(sigHeader, "sha256=") {
		return webhooks.VerifyHexHMACSHA256Prefixed(body, []byte(secret), sigHeader, "sha256=")
	}

	return hmac.Equal([]byte(sigHeader), []byte(secret))
}
