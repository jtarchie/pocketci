package webhooks_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/webhooks"
)

// noopProvider matches requests with an "X-Noop" header.
type noopProvider struct{ name string }

func (p *noopProvider) Name() string { return p.name }
func (p *noopProvider) Match(r *http.Request) bool {
	return r.Header.Get("X-Noop") == p.name
}
func (p *noopProvider) Parse(_ *http.Request, _ []byte, _ string) (*webhooks.Event, error) {
	return &webhooks.Event{Provider: p.name, EventType: "test"}, nil
}

// unauthorizedProvider always returns ErrUnauthorized from Parse.
type unauthorizedProvider struct{}

func (p *unauthorizedProvider) Name() string { return "unauth" }
func (p *unauthorizedProvider) Match(r *http.Request) bool {
	return r.Header.Get("X-Unauth") != ""
}
func (p *unauthorizedProvider) Parse(_ *http.Request, _ []byte, _ string) (*webhooks.Event, error) {
	return nil, webhooks.ErrUnauthorized
}

func TestDetect_FirstMatchWins(t *testing.T) {
	t.Parallel()
	providers := []webhooks.Provider{&noopProvider{"first"}, &noopProvider{"second"}}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Noop", "first")

	event, err := webhooks.Detect(providers, req, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "first" {
		t.Errorf("expected provider 'first', got %q", event.Provider)
	}
}

func TestDetect_NoMatch(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))

	_, err := webhooks.Detect(nil, req, nil, "")
	if err != webhooks.ErrNoMatch {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestDetect_UnauthorizedPropagated(t *testing.T) {
	t.Parallel()
	providers := []webhooks.Provider{&unauthorizedProvider{}}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Unauth", "yes")

	_, err := webhooks.Detect(providers, req, nil, "secret")
	if err != webhooks.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
