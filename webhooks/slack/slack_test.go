package slack_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/webhooks"
	"github.com/jtarchie/pocketci/webhooks/slack"
)

func sign(body []byte, secret, timestamp string) string {
	base := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))

	return fmt.Sprintf("v0=%x", mac.Sum(nil))
}

func TestSlack_Match(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"event_callback"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Slack-Signature", "v0=whatever")
	req.Header.Set("X-Slack-Request-Timestamp", "1609459200")

	event, err := webhooks.Detect([]webhooks.Provider{slack.New()}, req, body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "slack" {
		t.Errorf("expected provider 'slack', got %q", event.Provider)
	}

	if event.EventType != "event_callback" {
		t.Errorf("expected eventType 'event_callback', got %q", event.EventType)
	}
}

func TestSlack_ValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"url_verification","challenge":"abc123"}`)
	secret := "signing_secret"
	timestamp := "1609459200"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Slack-Signature", sign(body, secret, timestamp))
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)

	event, err := webhooks.Detect([]webhooks.Provider{slack.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.EventType != "url_verification" {
		t.Errorf("expected eventType 'url_verification', got %q", event.EventType)
	}
}

func TestSlack_InvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"event_callback"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Slack-Signature", "v0=badsig")
	req.Header.Set("X-Slack-Request-Timestamp", "1609459200")

	_, err := webhooks.Detect([]webhooks.Provider{slack.New()}, req, body, "signing_secret")
	if err != webhooks.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestSlack_MissingHeaders(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"event_callback"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Slack-Signature", "v0=something")
	// Missing X-Slack-Request-Timestamp

	_, err := webhooks.Detect([]webhooks.Provider{slack.New()}, req, body, "signing_secret")
	if err != webhooks.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
