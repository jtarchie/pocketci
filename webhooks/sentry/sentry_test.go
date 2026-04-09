package sentry_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/webhooks"
	"github.com/jtarchie/pocketci/webhooks/sentry"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return hex.EncodeToString(mac.Sum(nil))
}

func TestSentry_Match(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Sentry-Hook-Signature", "abc123")
	req.Header.Set("Sentry-Hook-Resource", "issue")

	event, err := webhooks.Detect([]webhooks.Provider{sentry.New()}, req, []byte(`{}`), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "sentry" {
		t.Errorf("expected provider 'sentry', got %q", event.Provider)
	}

	if event.EventType != "issue" {
		t.Errorf("expected eventType 'issue', got %q", event.EventType)
	}
}

func TestSentry_NoMatch(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No Sentry-Hook-Signature header

	_, err := webhooks.Detect([]webhooks.Provider{sentry.New()}, req, []byte("{}"), "")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestSentry_ValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"created","data":{}}`)
	secret := "mysecret"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Sentry-Hook-Signature", sign(body, secret))
	req.Header.Set("Sentry-Hook-Resource", "issue")

	event, err := webhooks.Detect([]webhooks.Provider{sentry.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "sentry" {
		t.Errorf("expected provider 'sentry', got %q", event.Provider)
	}

	if event.EventType != "issue" {
		t.Errorf("expected eventType 'issue' (from header), got %q", event.EventType)
	}
}

func TestSentry_EventTypeFromBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"resolved"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Sentry-Hook-Signature", "anysig")
	// No Sentry-Hook-Resource header

	event, err := webhooks.Detect([]webhooks.Provider{sentry.New()}, req, body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.EventType != "resolved" {
		t.Errorf("expected eventType 'resolved', got %q", event.EventType)
	}
}

func TestSentry_InvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"created"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Sentry-Hook-Signature", "badhex")

	_, err := webhooks.Detect([]webhooks.Provider{sentry.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestSentry_MissingSignatureWithSecret(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No Sentry-Hook-Signature

	_, err := webhooks.Detect([]webhooks.Provider{sentry.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch (no header = no match), got %v", err)
	}
}
