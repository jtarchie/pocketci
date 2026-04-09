package pagerduty_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/webhooks"
	"github.com/jtarchie/pocketci/webhooks/pagerduty"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestPagerDuty_Match(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-PagerDuty-Signature", "v1=abc123")

	event, err := webhooks.Detect([]webhooks.Provider{pagerduty.New()}, req, []byte(`{"event":"incident.triggered"}`), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "pagerduty" {
		t.Errorf("expected provider 'pagerduty', got %q", event.Provider)
	}

	if event.EventType != "incident.triggered" {
		t.Errorf("expected eventType 'incident.triggered', got %q", event.EventType)
	}
}

func TestPagerDuty_NoMatch(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No X-PagerDuty-Signature header

	_, err := webhooks.Detect([]webhooks.Provider{pagerduty.New()}, req, []byte("{}"), "")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestPagerDuty_ValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"messages":[{"event":"incident.triggered"}]}`)
	secret := "mysecret"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-PagerDuty-Signature", sign(body, secret))

	event, err := webhooks.Detect([]webhooks.Provider{pagerduty.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "pagerduty" {
		t.Errorf("expected provider 'pagerduty', got %q", event.Provider)
	}

	if event.EventType != "incident.triggered" {
		t.Errorf("expected eventType 'incident.triggered', got %q", event.EventType)
	}
}

func TestPagerDuty_MultipleSignatures(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"incident.resolved"}`)
	secret := "mysecret"

	// PagerDuty may send multiple v1= signatures for key rotation
	validSig := sign(body, secret)
	sigHeader := "v1=oldsignature," + validSig

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-PagerDuty-Signature", sigHeader)

	event, err := webhooks.Detect([]webhooks.Provider{pagerduty.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.EventType != "incident.resolved" {
		t.Errorf("expected eventType 'incident.resolved', got %q", event.EventType)
	}
}

func TestPagerDuty_InvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"incident.triggered"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-PagerDuty-Signature", "v1=badhex")

	_, err := webhooks.Detect([]webhooks.Provider{pagerduty.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestPagerDuty_MissingSignatureWithSecret(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No X-PagerDuty-Signature

	_, err := webhooks.Detect([]webhooks.Provider{pagerduty.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch (no header = no match), got %v", err)
	}
}
