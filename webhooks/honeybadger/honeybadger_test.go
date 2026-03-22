package honeybadger_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/webhooks"
	"github.com/jtarchie/pocketci/webhooks/honeybadger"
)

func TestHoneybadger_MatchAndValidToken(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"check_in"}`)
	secret := "my-secret"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Honeybadger-Token", secret)

	event, err := webhooks.Detect([]webhooks.Provider{honeybadger.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "honeybadger" {
		t.Errorf("expected provider 'honeybadger', got %q", event.Provider)
	}

	if event.EventType != "check_in" {
		t.Errorf("expected eventType 'check_in', got %q", event.EventType)
	}
}

func TestHoneybadger_MissingTokenNoDetection(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"check_in"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))

	_, err := webhooks.Detect([]webhooks.Provider{honeybadger.New()}, req, body, "my-secret")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestHoneybadger_InvalidToken(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"check_in"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Honeybadger-Token", "wrong-token")

	_, err := webhooks.Detect([]webhooks.Provider{honeybadger.New()}, req, body, "my-secret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestHoneybadger_EmptySecretIsUnauthorized(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"uptime"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Honeybadger-Token", "some-token")

	_, err := webhooks.Detect([]webhooks.Provider{honeybadger.New()}, req, body, "")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestHoneybadger_EventTypeFallbackToEvent(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"uptime.alert"}`)
	secret := "my-secret"

	req := httptest.NewRequest(http.MethodPost, "/?source=honeybadger", strings.NewReader(""))
	req.Header.Set("Honeybadger-Token", secret)

	event, err := webhooks.Detect([]webhooks.Provider{honeybadger.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.EventType != "uptime.alert" {
		t.Errorf("expected eventType 'uptime.alert', got %q", event.EventType)
	}

	if event.Query["source"] != "honeybadger" {
		t.Errorf("expected query source 'honeybadger', got %q", event.Query["source"])
	}
}
