package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	nhttp "github.com/jtarchie/pocketci/runtime/jsapi/notifiers/http"
	. "github.com/onsi/gomega"
)

func TestNotifyRenderTemplate(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, nil)

	// Set up context
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "test-pipeline",
		JobName:      "test-job",
		BuildID:      "123",
		Status:       "success",
	})

	// Test basic template rendering (using struct field names, not JSON names)
	result, err := notifier.RenderTemplate("Build {{ .JobName }} completed with status {{ .Status }}")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).To(Equal("Build test-job completed with status success"))

	// Test with Sprig functions
	result, err = notifier.RenderTemplate("Pipeline: {{ .PipelineName | upper }}")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).To(Equal("Pipeline: TEST-PIPELINE"))
}

func TestNotifyContextUpdates(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, nil)

	// Set initial context
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "my-pipeline",
		JobName:      "job-1",
		Status:       "pending",
	})

	// Update job name using UpdateContext
	notifier.UpdateContext(func(ctx *jsapi.NotifyContext) {
		ctx.JobName = "job-2"
	})

	result, err := notifier.RenderTemplate("Job: {{ .JobName }}")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).To(Equal("Job: job-2"))

	// Update status
	notifier.UpdateContext(func(ctx *jsapi.NotifyContext) {
		ctx.Status = "running"
	})

	result, err = notifier.RenderTemplate("Status: {{ .Status }}")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result).To(Equal("Status: running"))
}

func TestNotifyConfigLookup(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, nil)

	// Set configs using map
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"slack-channel": {
			Type:     "slack",
			Token:    "xoxb-token",
			Channels: []string{"#builds"},
		},
		"teams-webhook": {
			Type:    "teams",
			Webhook: "https://teams.webhook.url",
		},
	})

	// Test config lookup by name
	config, exists := notifier.GetConfig("slack-channel")
	assert.Expect(exists).To(BeTrue())
	assert.Expect(config.Type).To(Equal("slack"))
	assert.Expect(config.Token).To(Equal("xoxb-token"))

	config, exists = notifier.GetConfig("teams-webhook")
	assert.Expect(exists).To(BeTrue())
	assert.Expect(config.Type).To(Equal("teams"))

	// Test missing config
	_, exists = notifier.GetConfig("nonexistent")
	assert.Expect(exists).To(BeFalse())
}

func TestNotifySetConfigs(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, nil)

	// Initially empty
	_, exists := notifier.GetConfig("test")
	assert.Expect(exists).To(BeFalse())

	// Set configs
	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"test": {
			Type: "http",
			URL:  "https://example.com/webhook",
		},
	})

	config, exists := notifier.GetConfig("test")
	assert.Expect(exists).To(BeTrue())
	assert.Expect(config.Type).To(Equal("http"))
}

func TestNotifyHTTPIntegration(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Track received notifications
	var mu sync.Mutex
	var receivedRequests []struct {
		Method      string
		ContentType string
		Body        map[string]string
		Headers     http.Header
	}

	// Start a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}

		var payload map[string]string
		err = json.Unmarshal(body, &payload)
		if err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		mu.Lock()
		receivedRequests = append(receivedRequests, struct {
			Method      string
			ContentType string
			Body        map[string]string
			Headers     http.Header
		}{
			Method:      r.Method,
			ContentType: r.Header.Get("Content-Type"),
			Body:        payload,
			Headers:     r.Header.Clone(),
		})
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create notifier with HTTP config pointing to test server
	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, []jsapi.Adapter{nhttp.New()})

	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"test-webhook": {
			Type:   "http",
			URL:    server.URL,
			Method: "POST",
			Headers: map[string]string{
				"X-Custom-Header": "test-value",
				"Authorization":   "Bearer test-token",
			},
		},
	})

	// Set up context for template rendering
	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "integration-test-pipeline",
		JobName:      "build-job",
		BuildID:      "build-123",
		Status:       "success",
		StartTime:    "2026-01-10T12:00:00Z",
		EndTime:      "2026-01-10T12:05:00Z",
		Duration:     "5m0s",
		Environment: map[string]string{
			"BRANCH": "main",
		},
		TaskResults: map[string]any{},
	})

	// Send a notification with template
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := notifier.Send(ctx, "test-webhook", "Build {{ .JobName }} completed: {{ .Status | upper }}")
	assert.Expect(err).NotTo(HaveOccurred())

	// Verify the server received the notification
	mu.Lock()
	defer mu.Unlock()

	assert.Expect(receivedRequests).To(HaveLen(1))

	req := receivedRequests[0]
	assert.Expect(req.Method).To(Equal("POST"))
	assert.Expect(req.ContentType).To(Equal("application/json"))
	assert.Expect(req.Headers.Get("X-Custom-Header")).To(Equal("test-value"))
	assert.Expect(req.Headers.Get("Authorization")).To(Equal("Bearer test-token"))

	// Verify the payload contains rendered message
	assert.Expect(req.Body["subject"]).To(Equal("Pipeline Notification"))
	assert.Expect(req.Body["message"]).To(Equal("Build build-job completed: SUCCESS"))
}

func TestNotifyHTTPIntegrationWithMultipleNotifications(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Track received notifications
	var mu sync.Mutex
	var receivedCount int

	// Start a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedCount++
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create notifier with multiple HTTP configs
	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, []jsapi.Adapter{nhttp.New()})

	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"webhook-1": {
			Type: "http",
			URL:  server.URL + "/webhook1",
		},
		"webhook-2": {
			Type: "http",
			URL:  server.URL + "/webhook2",
		},
	})

	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "multi-notify-pipeline",
		JobName:      "deploy",
		Status:       "success",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send to first webhook
	err := notifier.Send(ctx, "webhook-1", "First notification")
	assert.Expect(err).NotTo(HaveOccurred())

	// Send to second webhook
	err = notifier.Send(ctx, "webhook-2", "Second notification")
	assert.Expect(err).NotTo(HaveOccurred())

	// Verify both were received
	mu.Lock()
	defer mu.Unlock()
	assert.Expect(receivedCount).To(Equal(2))
}

func TestNotifyHTTPIntegrationServerError(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Start a test HTTP server that returns errors
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, []jsapi.Adapter{nhttp.New()})

	notifier.SetConfigs(map[string]jsapi.NotifyConfig{
		"failing-webhook": {
			Type: "http",
			URL:  server.URL,
		},
	})

	notifier.SetContext(jsapi.NotifyContext{
		PipelineName: "error-test",
		Status:       "failure",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send should fail due to server error
	err := notifier.Send(ctx, "failing-webhook", "This should fail")
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("could not send notification"))
}

func TestNotifyHTTPIntegrationMissingConfig(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.Default()
	notifier := jsapi.NewNotifier(logger, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send to non-existent config should fail
	err := notifier.Send(ctx, "nonexistent-webhook", "This should fail")
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("notification config \"nonexistent-webhook\" not found"))
}
