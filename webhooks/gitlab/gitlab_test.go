package gitlab_test

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
	"github.com/jtarchie/pocketci/webhooks/gitlab"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}

func TestGitLab_Match(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Gitlab-Event", "Push Hook")

	event, err := webhooks.Detect([]webhooks.Provider{gitlab.New()}, req, []byte("{}"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "gitlab" {
		t.Errorf("expected provider 'gitlab', got %q", event.Provider)
	}

	if event.EventType != "Push Hook" {
		t.Errorf("expected eventType 'Push Hook', got %q", event.EventType)
	}
}

func TestGitLab_NoMatch(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	// No X-Gitlab-Event header

	_, err := webhooks.Detect([]webhooks.Provider{gitlab.New()}, req, []byte("{}"), "")
	if !errors.Is(err, webhooks.ErrNoMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

func TestGitLab_ValidHMACSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"object_kind":"push"}`)
	secret := "mysecret"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Token", sign(body, secret))

	event, err := webhooks.Detect([]webhooks.Provider{gitlab.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "gitlab" {
		t.Errorf("expected provider 'gitlab', got %q", event.Provider)
	}

	if event.EventType != "Push Hook" {
		t.Errorf("expected eventType 'Push Hook', got %q", event.EventType)
	}
}

func TestGitLab_ValidPlainToken(t *testing.T) {
	t.Parallel()
	body := []byte(`{"object_kind":"push"}`)
	secret := "mysecret"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Token", secret) // plain token, not HMAC

	event, err := webhooks.Detect([]webhooks.Provider{gitlab.New()}, req, body, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Provider != "gitlab" {
		t.Errorf("expected provider 'gitlab', got %q", event.Provider)
	}
}

func TestGitLab_InvalidSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"object_kind":"push"}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	req.Header.Set("X-Gitlab-Token", "sha256=badhex")

	_, err := webhooks.Detect([]webhooks.Provider{gitlab.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestGitLab_MissingSignatureWithSecret(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	// No X-Gitlab-Token

	_, err := webhooks.Detect([]webhooks.Provider{gitlab.New()}, req, body, "mysecret")
	if !errors.Is(err, webhooks.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
