package runtime_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	nhttp "github.com/jtarchie/pocketci/runtime/jsapi/notifiers/http"
	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	. "github.com/onsi/gomega"
)

// mapSecretsManager is a simple in-memory secrets.Manager for testing.
type mapSecretsManager struct {
	data map[string]string // "scope/key" -> value
}

func newMapSecretsManager(values map[string]string) *mapSecretsManager {
	return &mapSecretsManager{data: values}
}

func (m *mapSecretsManager) Get(_ context.Context, scope, key string) (string, error) {
	if v, ok := m.data[scope+"/"+key]; ok {
		return v, nil
	}

	return "", secrets.ErrNotFound
}

func (m *mapSecretsManager) Set(_ context.Context, scope, key, value string) error {
	m.data[scope+"/"+key] = value
	return nil
}

func (m *mapSecretsManager) Delete(_ context.Context, scope, key string) error {
	delete(m.data, scope+"/"+key)
	return nil
}

func (m *mapSecretsManager) ListByScope(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (m *mapSecretsManager) DeleteByScope(_ context.Context, _ string) error {
	return nil
}

func (m *mapSecretsManager) Close() error { return nil }

// TestNotifierSecretResolutionInURL verifies that a "secret:" reference in the
// HTTP Notification URL is resolved before the request is dispatched.
func TestNotifierSecretResolutionInURL(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := slog.New(slog.DiscardHandler)
	notifier := jsapi.NewNotifier(logger, []jsapi.Adapter{nhttp.New()})

	mgr := newMapSecretsManager(map[string]string{
		"pipeline/pipe1/WEBHOOK_URL": server.URL,
	})
	notifier.SetSecretsManager(mgr, "pipe1")
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"http-url-secret": {
			Type:   "http",
			URL:    "secret:WEBHOOK_URL",
			Method: "POST",
		},
	})

	err := notifier.Send(context.Background(), "http-url-secret", "hello")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(called).To(BeTrue(), "HTTP server should have been called with resolved URL")
}

// TestNotifierSecretResolutionInHeaders verifies that "secret:" references in
// notification Headers are resolved before the request is dispatched.
func TestNotifierSecretResolutionInHeaders(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := slog.New(slog.DiscardHandler)
	notifier := jsapi.NewNotifier(logger, []jsapi.Adapter{nhttp.New()})

	mgr := newMapSecretsManager(map[string]string{
		"pipeline/pipe1/API_TOKEN": "my-resolved-token",
	})
	notifier.SetSecretsManager(mgr, "pipe1")
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"http-header-secret": {
			Type:   "http",
			URL:    server.URL,
			Method: "POST",
			Headers: map[string]string{
				"Authorization": "secret:API_TOKEN",
			},
		},
	})

	err := notifier.Send(context.Background(), "http-header-secret", "hello")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(capturedAuth).To(Equal("my-resolved-token"),
		"Authorization header should contain the resolved secret value")
}

// TestNotifierSecretMissingReturnsError verifies that a missing secret causes
// Send to return a descriptive error instead of using the raw "secret:" string.
func TestNotifierSecretMissingReturnsError(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)
	notifier := jsapi.NewNotifier(logger, nil)

	mgr := newMapSecretsManager(map[string]string{}) // empty — secret not stored
	notifier.SetSecretsManager(mgr, "pipe1")
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"slack-missing": {
			Type:  "slack",
			Token: "secret:MISSING_SLACK_TOKEN",
		},
	})

	err := notifier.Send(context.Background(), "slack-missing", "hello")
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("MISSING_SLACK_TOKEN"))
}

// TestResourceRunnerSecretResolutionRecursive verifies that nested map values
// with "secret:<KEY>" references are resolved, and non-string types pass through
// unchanged. Secret resolution runs before the resource type is looked up, so a
// successful resolution followed by a "resource type not found" error proves the
// resolver executed without issues.
func TestResourceRunnerSecretResolutionRecursive(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)
	rr := runner.NewResourceRunner(context.Background(), logger, nil)

	mgr := newMapSecretsManager(map[string]string{
		"pipeline/pipe1/DB_PASS": "s3cr3t",
	})
	rr.SetSecretsManager(mgr, "pipe1")

	// The type "nonexistent-type" is not registered; we expect
	// "resource type not found", which means secret resolution succeeded.
	_, err := rr.Check(runner.ResourceCheckInput{
		Type: "nonexistent-type",
		Source: map[string]any{
			"nested": map[string]any{
				"password": "secret:DB_PASS",
				"port":     5432,
				"enabled":  true,
			},
		},
	})
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("resource type not found"),
		"secret resolution should succeed; only the unregistered resource type should cause an error")
}

// TestResourceRunnerSecretMissingReturnsError verifies that a missing secret
// causes ResourceRunner.Check/Fetch/Push to return a descriptive error.
func TestResourceRunnerSecretMissingReturnsError(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)
	rr := runner.NewResourceRunner(context.Background(), logger, nil)

	mgr := newMapSecretsManager(map[string]string{}) // no secrets stored
	rr.SetSecretsManager(mgr, "pipe1")

	_, err := rr.Check(runner.ResourceCheckInput{
		Type:   "git",
		Source: map[string]any{"token": "secret:GH_TOKEN"},
	})
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("GH_TOKEN"))
}

// TestResourceRunnerFetchSecretMissing verifies Fetch also resolves secrets.
func TestResourceRunnerFetchSecretMissing(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)
	rr := runner.NewResourceRunner(context.Background(), logger, nil)

	mgr := newMapSecretsManager(map[string]string{})
	rr.SetSecretsManager(mgr, "pipe1")

	_, err := rr.Fetch(runner.ResourceFetchInput{
		Type:    "git",
		Source:  map[string]any{"password": "secret:GH_PASSWORD"},
		Version: map[string]string{"ref": "abc123"},
		DestDir: t.TempDir(),
	})
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("GH_PASSWORD"))
}

// TestResourceRunnerPushSecretMissing verifies Push also resolves secrets.
func TestResourceRunnerPushSecretMissing(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)
	rr := runner.NewResourceRunner(context.Background(), logger, nil)

	mgr := newMapSecretsManager(map[string]string{})
	rr.SetSecretsManager(mgr, "pipe1")

	_, err := rr.Push(runner.ResourcePushInput{
		Type:   "s3",
		Source: map[string]any{"secret_key": "secret:S3_SECRET"},
		SrcDir: t.TempDir(),
	})
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("S3_SECRET"))
}

// TestResolveSecretStringRejectsSystemKeys verifies that system-managed secret
// keys (driver, webhook_secret) cannot be read through the secret: prefix.
func TestResolveSecretStringRejectsSystemKeys(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	mgr := newMapSecretsManager(map[string]string{
		"pipeline/pipe1/driver":         "docker",
		"pipeline/pipe1/webhook_secret": "super-secret-webhook",
	})

	for _, key := range []string{"driver", "webhook_secret"} {
		_, _, err := support.ResolveSecretString(context.Background(), mgr, "pipe1", "secret:"+key)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("reserved for system use"))
	}
}
