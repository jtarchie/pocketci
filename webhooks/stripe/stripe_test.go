package stripe_test

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
	"github.com/jtarchie/pocketci/webhooks/stripe"
)

func sign(body []byte, secret, timestamp string) string {
	signed := fmt.Sprintf("%s.%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))

	return fmt.Sprintf("t=%s,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

func TestStripe_Match(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Stripe-Signature", "t=1234,v1=abc")

	event, err := webhooks.Detect([]webhooks.Provider{stripe.New()}, req, []byte(`{"type":"payment_intent.created"}`), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "stripe" {
		t.Errorf("expected provider 'stripe', got %q", event.Provider)
	}

	if event.EventType != "payment_intent.created" {
		t.Errorf("expected eventType 'payment_intent.created', got %q", event.EventType)
	}
}

func TestStripe_NoMatch(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No Stripe-Signature header

	_, err := webhooks.Detect([]webhooks.Provider{stripe.New()}, req, []byte("{}"), "")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestStripe_ValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"charge.succeeded","id":"evt_123"}`)
	secret := "whsec_mysecret"
	timestamp := "1609459200"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Stripe-Signature", sign(body, secret, timestamp))

	event, err := webhooks.Detect([]webhooks.Provider{stripe.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "stripe" {
		t.Errorf("expected provider 'stripe', got %q", event.Provider)
	}

	if event.EventType != "charge.succeeded" {
		t.Errorf("expected eventType 'charge.succeeded', got %q", event.EventType)
	}
}

func TestStripe_InvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"charge.succeeded"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Stripe-Signature", "t=1609459200,v1=badhex")

	_, err := webhooks.Detect([]webhooks.Provider{stripe.New()}, req, body, "whsec_mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestStripe_MissingSignatureWithSecret(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No Stripe-Signature

	_, err := webhooks.Detect([]webhooks.Provider{stripe.New()}, req, body, "whsec_mysecret")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch (no header = no match), got %v", err)
	}
}

func TestStripe_MalformedSignatureHeader(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Stripe-Signature", "notvalid")

	_, err := webhooks.Detect([]webhooks.Provider{stripe.New()}, req, body, "whsec_mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
