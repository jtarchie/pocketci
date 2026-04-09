package bitbucket_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/webhooks"
	"github.com/jtarchie/pocketci/webhooks/bitbucket"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}

func TestBitbucket_Match(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Event-Key", "repo:push")

	event, err := webhooks.Detect([]webhooks.Provider{bitbucket.New()}, req, []byte("{}"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "bitbucket" {
		t.Errorf("expected provider 'bitbucket', got %q", event.Provider)
	}

	if event.EventType != "repo:push" {
		t.Errorf("expected eventType 'repo:push', got %q", event.EventType)
	}
}

func TestBitbucket_NoMatch(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No X-Event-Key header

	_, err := webhooks.Detect([]webhooks.Provider{bitbucket.New()}, req, []byte("{}"), "")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestBitbucket_ValidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"actor":{"display_name":"test"}}`)
	secret := "mysecret"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Event-Key", "repo:push")
	req.Header.Set("X-Hub-Signature", sign(body, secret))

	event, err := webhooks.Detect([]webhooks.Provider{bitbucket.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "bitbucket" {
		t.Errorf("expected provider 'bitbucket', got %q", event.Provider)
	}

	if event.EventType != "repo:push" {
		t.Errorf("expected eventType 'repo:push', got %q", event.EventType)
	}
}

func TestBitbucket_InvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"actor":{"display_name":"test"}}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Event-Key", "repo:push")
	req.Header.Set("X-Hub-Signature", "sha256=badhex")

	_, err := webhooks.Detect([]webhooks.Provider{bitbucket.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestBitbucket_MissingSignatureWithSecret(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Event-Key", "repo:push")
	// No X-Hub-Signature

	_, err := webhooks.Detect([]webhooks.Provider{bitbucket.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestBitbucket_MissingHashPrefix(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Event-Key", "repo:push")
	req.Header.Set("X-Hub-Signature", "noprefixhere")

	_, err := webhooks.Detect([]webhooks.Provider{bitbucket.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
