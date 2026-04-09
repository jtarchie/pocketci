package linear_test

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
	"github.com/jtarchie/pocketci/webhooks/linear"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return hex.EncodeToString(mac.Sum(nil))
}

func TestLinear_Match(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Linear-Signature", "abc123")

	event, err := webhooks.Detect([]webhooks.Provider{linear.New()}, req, []byte(`{"type":"Issue","action":"create"}`), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "linear" {
		t.Errorf("expected provider 'linear', got %q", event.Provider)
	}

	if event.EventType != "Issue" {
		t.Errorf("expected eventType 'Issue', got %q", event.EventType)
	}
}

func TestLinear_NoMatch(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No Linear-Signature header

	_, err := webhooks.Detect([]webhooks.Provider{linear.New()}, req, []byte("{}"), "")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestLinear_ValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"Issue","action":"update"}`)
	secret := "mysecret"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Linear-Signature", sign(body, secret))

	event, err := webhooks.Detect([]webhooks.Provider{linear.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "linear" {
		t.Errorf("expected provider 'linear', got %q", event.Provider)
	}

	if event.EventType != "Issue" {
		t.Errorf("expected eventType 'Issue', got %q", event.EventType)
	}
}

func TestLinear_EventTypeFromAction(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"remove"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Linear-Signature", "anysig")

	event, err := webhooks.Detect([]webhooks.Provider{linear.New()}, req, body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.EventType != "remove" {
		t.Errorf("expected eventType 'remove', got %q", event.EventType)
	}
}

func TestLinear_InvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"Issue"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Linear-Signature", "badhex")

	_, err := webhooks.Detect([]webhooks.Provider{linear.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestLinear_MissingSignatureWithSecret(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No Linear-Signature

	_, err := webhooks.Detect([]webhooks.Provider{linear.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch (no header = no match), got %v", err)
	}
}
