package runtime_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/runtime"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	sqliteStorage "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func runWithWebhookAndStore(t *testing.T, src string, data *jsapi.WebhookData, pipelineID string) error {
	t.Helper()

	store, err := sqliteStorage.NewSqlite(sqliteStorage.Config{Path: ":memory:"}, "test-ns", nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Close() }()

	js := runtime.NewJS(slog.Default())

	return js.ExecuteWithOptions(context.Background(), src, nil, store, runtime.ExecuteOptions{
		WebhookData: data,
		PipelineID:  pipelineID,
	})
}

func TestWebhookDedupGlobal(t *testing.T) {
	t.Parallel()

	t.Run("returns false for manual trigger (nil WebhookData)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		err := runWithWebhookAndStore(t, `
			async function pipeline() {
				assert.equal(webhookDedup('provider'), false);
			}
			export { pipeline };
		`, nil, "pipeline-1")
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("returns false on first call, true on second call with same key", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, err := sqliteStorage.NewSqlite(sqliteStorage.Config{Path: ":memory:"}, "test-ns", nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		// Create a pipeline so the FK constraint is satisfied
		pipeline, err := store.SavePipeline(context.Background(), "test-dedup", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		webhookData := &jsapi.WebhookData{
			Provider:  "github",
			EventType: "push",
			Method:    "POST",
			Headers:   map[string]string{"X-GitHub-Delivery": "abc-123"},
			Query:     map[string]string{},
			Body:      "{}",
		}

		js := runtime.NewJS(slog.Default())

		// First execution: not a duplicate
		err = js.ExecuteWithOptions(context.Background(), `
			async function pipeline() {
				assert.equal(webhookDedup('headers["X-GitHub-Delivery"]'), false);
			}
			export { pipeline };
		`, nil, store, runtime.ExecuteOptions{
			WebhookData: webhookData,
			PipelineID:  pipeline.ID,
		})
		assert.Expect(err).NotTo(HaveOccurred())

		// Second execution with same data: is a duplicate
		err = js.ExecuteWithOptions(context.Background(), `
			async function pipeline() {
				assert.equal(webhookDedup('headers["X-GitHub-Delivery"]'), true);
			}
			export { pipeline };
		`, nil, store, runtime.ExecuteOptions{
			WebhookData: webhookData,
			PipelineID:  pipeline.ID,
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("returns false for different keys", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		store, err := sqliteStorage.NewSqlite(sqliteStorage.Config{Path: ":memory:"}, "test-ns", nil)
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = store.Close() }()

		pipeline, err := store.SavePipeline(context.Background(), "test-dedup", "content", "docker", "")
		assert.Expect(err).NotTo(HaveOccurred())

		js := runtime.NewJS(slog.Default())

		// First webhook
		err = js.ExecuteWithOptions(context.Background(), `
			async function pipeline() {
				assert.equal(webhookDedup('headers["X-GitHub-Delivery"]'), false);
			}
			export { pipeline };
		`, nil, store, runtime.ExecuteOptions{
			WebhookData: &jsapi.WebhookData{
				Provider: "github", EventType: "push", Method: "POST",
				Headers: map[string]string{"X-GitHub-Delivery": "first"},
				Query:   map[string]string{}, Body: "{}",
			},
			PipelineID: pipeline.ID,
		})
		assert.Expect(err).NotTo(HaveOccurred())

		// Different webhook — should not be a duplicate
		err = js.ExecuteWithOptions(context.Background(), `
			async function pipeline() {
				assert.equal(webhookDedup('headers["X-GitHub-Delivery"]'), false);
			}
			export { pipeline };
		`, nil, store, runtime.ExecuteOptions{
			WebhookData: &jsapi.WebhookData{
				Provider: "github", EventType: "push", Method: "POST",
				Headers: map[string]string{"X-GitHub-Delivery": "second"},
				Query:   map[string]string{}, Body: "{}",
			},
			PipelineID: pipeline.ID,
		})
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("returns false on invalid expression (does not skip)", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		err := runWithWebhookAndStore(t, `
			async function pipeline() {
				assert.equal(webhookDedup('nonexistent_var'), false);
			}
			export { pipeline };
		`, &jsapi.WebhookData{
			Provider: "github", EventType: "push", Method: "POST",
			Headers: map[string]string{}, Query: map[string]string{}, Body: "{}",
		}, "pipeline-1")
		assert.Expect(err).NotTo(HaveOccurred())
	})
}
