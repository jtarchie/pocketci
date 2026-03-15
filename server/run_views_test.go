package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestRunViews(t *testing.T) {
	t.Parallel()

	storage.Each(func(name string, init storage.InitFunc) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			t.Run("GET /runs/:id/tasks returns HTML with task tree", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				// Create a pipeline and run
				pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Store some task data at the expected path
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/jobs/test-job", map[string]any{"status": "success"})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				assert.Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "h1", "Tasks")).To(BeTrue())
				assert.Expect(doc.Find(`details[data-task-id*="test-job"]`).Length()).To(BeNumerically(">", 0))
			})

			t.Run("GET /runs/:id/graph returns HTML with graph view", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				// Create a pipeline and run
				pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Store some task data at the expected path
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/jobs/test-job", map[string]any{"status": "success", "dependsOn": []string{}})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/graph", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				assert.Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "h1", "Task Graph")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "script#graph-data", "test-job")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks shows run error alert when error_message is set", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "error-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusFailed, "failed to create volume: unauthorized")
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "[role='alert']", "Run failed")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "[role='alert']", "failed to create volume: unauthorized")).To(BeTrue())
			})

			t.Run("GET /runs/:id/graph shows run error alert when error_message is set", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "graph-error-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/jobs/test-job", map[string]any{"status": "failure", "dependsOn": []string{}})
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusFailed, "failed to create volume: unauthorized")
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/graph", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "[role='alert']", "Run failed")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "[role='alert']", "failed to create volume: unauthorized")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks returns empty tree for non-existent run", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/non-existent-run/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				// Should still return 200 with empty tree
				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				assert.Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
			})

			t.Run("GET /runs/:id/tasks includes RunID in template data", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				// Create a pipeline and run
				pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				// The template should show "Run <runID>" in breadcrumb
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "nav[aria-label='Breadcrumb'] a", "Run "+run.ID)).To(BeTrue())
			})

			t.Run("GET /runs/:id/graph includes correct link to tasks view", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				// Create a pipeline and run
				pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/graph", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				// The template should have a link to /runs/:id/tasks
				doc := mustHTMLDocument(t, rec)
				assert.Expect(selectorHasAttrValue(doc, "a", "href", "/runs/"+run.ID+"/tasks")).To(BeTrue())
				assert.Expect(selectorHasAttrValue(doc, "a", "href", "/api/runs/"+run.ID+"/status")).To(BeTrue())
				assert.Expect(selectorHasAttrValue(doc, "a", "href", "/api/runs/"+run.ID+"/tasks")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks shows execution number for single task", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "k6-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Single task - mirrors the k6 pipeline structure
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-k6", map[string]any{"status": "success"})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "*", "no value")).To(BeFalse())
				assert.Expect(hasSelectorWithText(doc, `details[data-task-id*="tasks/0-k6"] span.w-6.h-6`, "1")).To(BeTrue())
				assert.Expect(selectorHasAttrValue(doc, "a", "href", "/api/runs/"+run.ID+"/status")).To(BeTrue())
				assert.Expect(selectorHasAttrValue(doc, "a", "href", "/api/runs/"+run.ID+"/tasks")).To(BeTrue())
				assert.Expect(selectorHasAttrContaining(doc, "a", "href", "/api/runs/"+run.ID+"/tasks?path=")).To(BeTrue())
				assert.Expect(selectorHasAttrContaining(doc, "a", "href", "0-k6")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks shows correct execution numbers for multiple tasks", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "multi-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-task-a", map[string]any{"status": "success"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/1-task-b", map[string]any{"status": "success"})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "*", "no value")).To(BeFalse())
				assert.Expect(hasSelectorWithText(doc, `details[data-task-id*="tasks/0-task-a"] span.w-6.h-6`, "1")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, `details[data-task-id*="tasks/1-task-b"] span.w-6.h-6`, "2")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks includes correct link to graph view", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				// Create a pipeline and run
				pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				// The template should have a link to /runs/:id/graph
				doc := mustHTMLDocument(t, rec)
				assert.Expect(selectorHasAttrValue(doc, "a", "href", "/runs/"+run.ID+"/graph")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks-partial/ shows agent usage badge when usage field is present", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "agent-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Agent task payload includes a usage sub-object plus timing fields
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-review", map[string]any{
					"status":     "success",
					"started_at": "2026-03-06T10:00:00Z",
					"elapsed":    "42s",
					"usage": map[string]any{
						"promptTokens":     1000,
						"completionTokens": 250,
						"totalTokens":      1250,
						"llmRequests":      3,
						"toolCallCount":    5,
					},
				})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks-partial/", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				// Usage badge must appear with tool count and token total
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "tools")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "tok")).To(BeTrue())
				// Elapsed time must also appear
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "42s")).To(BeTrue())
				// Must not leak template errors
				assert.Expect(hasSelectorWithText(doc, "*", "no value")).To(BeFalse())
			})

			t.Run("GET /runs/:id/tasks-partial/ shows no usage badge for regular tasks", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "regular-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Regular task — no usage field
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{
					"status":  "success",
					"elapsed": "1.2s",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks-partial/", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				// No usage badge should appear
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "tok")).To(BeFalse())
				// Elapsed time should still show
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "1.2s")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "*", "no value")).To(BeFalse())
			})

			t.Run("GET /runs/:id/tasks-partial/ emits OOB header updates for active runs", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "active-run-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{"status": "success"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/1-test", map[string]any{"status": "failure"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/2-deploy", map[string]any{"status": "running"})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks-partial/", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(doc.Find(`#run-live-badge[hx-swap-oob="true"]`).Length()).To(BeNumerically(">", 0))
				assert.Expect(doc.Find(`button#run-stop-button[hx-swap-oob="true"]`).Length()).To(BeNumerically(">", 0))
				assert.Expect(hasSelectorWithText(doc, `#stat-success[hx-swap-oob="true"][aria-label="Successful tasks"]`, "1")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, `#stat-failure[hx-swap-oob="true"][aria-label="Failed tasks"]`, "1")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, `#stat-pending[hx-swap-oob="true"][aria-label="Pending tasks"]`, "1")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "#run-live-badge", "Live")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "#run-stop-button", "Stop Run")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks-partial/ clears active header updates when run completes", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "completed-run-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{"status": "success"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/1-test", map[string]any{"status": "failure"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/2-deploy", map[string]any{"status": "pending"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks-partial/", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(286))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(doc.Find(`#run-live-badge[hx-swap-oob="true"]`).Length()).To(BeNumerically(">", 0))
				assert.Expect(doc.Find(`span#run-stop-button[hx-swap-oob="true"]`).Length()).To(BeNumerically(">", 0))
				assert.Expect(hasSelectorWithText(doc, `#stat-success[hx-swap-oob="true"][aria-label="Successful tasks"]`, "1")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, `#stat-failure[hx-swap-oob="true"][aria-label="Failed tasks"]`, "1")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, `#stat-pending[hx-swap-oob="true"][aria-label="Pending tasks"]`, "1")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "#run-live-badge", "Live")).To(BeFalse())
				assert.Expect(doc.Find("button#run-stop-button").Length()).To(Equal(0))
			})

			// Regression tests: the initial /runs/:id/tasks page (Show handler) previously
			// omitted "usage" from its GetAll field list, so the usage badge and elapsed
			// were invisible on first load even though tasks-partial/ showed them correctly.
			t.Run("GET /runs/:id/tasks shows agent usage badge on initial page load", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "agent-pipeline-show", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-agent", map[string]any{
					"status":     "success",
					"started_at": "2026-03-06T10:00:00Z",
					"elapsed":    "7s",
					"usage": map[string]any{
						"promptTokens":     500,
						"completionTokens": 100,
						"totalTokens":      600,
						"llmRequests":      2,
						"toolCallCount":    4,
					},
				})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				// Usage badge (tools · tok) must appear on the initial page load
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "tools")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "tok")).To(BeTrue())
				// Elapsed time must appear
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "7s")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "*", "no value")).To(BeFalse())
			})

			t.Run("GET /runs/:id/tasks shows no usage badge for regular tasks on initial page load", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "regular-pipeline-show", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Regular task — no usage field
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{
					"status":  "success",
					"elapsed": "3s",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				// No usage badge for a plain task
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "tok")).To(BeFalse())
				// Elapsed time should still appear
				assert.Expect(hasSelectorWithText(doc, ".font-mono", "3s")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "*", "no value")).To(BeFalse())
			})

			t.Run("GET /runs/:id/tasks renders stderr-only task logs", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "stderr-only-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-lint", map[string]any{
					"status": "failure",
					"logs": []map[string]any{
						{"type": "stderr", "content": "lint failed: missing semicolon"},
					},
				})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "div[id^='terminal-']", "lint failed: missing semicolon")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks renders skipped status for skipped tasks", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "skipped-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/jobs/build/0/tasks/step-a", map[string]any{"status": "failure"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/jobs/build/1/tasks/step-b", map[string]any{"status": "skipped"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/jobs/build/2/tasks/step-c", map[string]any{"status": "skipped"})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(hasSelectorWithText(doc, "span.sr-only", "Skipped")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "span.sr-only", "Failed")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks-partial/ emits OOB error alert when run has error message", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "oob-error-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{"status": "failure"})
				assert.Expect(err).NotTo(HaveOccurred())
				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusFailed, "agent config unmarshal error")
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks-partial/", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(286))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(doc.Find(`#run-error-alert[hx-swap-oob="true"]`).Length()).To(BeNumerically(">", 0))
				assert.Expect(hasSelectorWithText(doc, "[role='alert']", "Run failed")).To(BeTrue())
				assert.Expect(hasSelectorWithText(doc, "[role='alert']", "agent config unmarshal error")).To(BeTrue())
			})

			t.Run("GET /runs/:id/tasks-partial/ emits empty OOB error alert when no error", func(t *testing.T) {
				t.Parallel()
				assert := NewGomegaWithT(t)

				buildFile, err := os.CreateTemp(t.TempDir(), "")
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = buildFile.Close() }()

				client, err := init(buildFile.Name(), "namespace", slog.Default())
				assert.Expect(err).NotTo(HaveOccurred())
				defer func() { _ = client.Close() }()

				pipeline, err := client.SavePipeline(context.Background(), "oob-no-error-pipeline", "export const pipeline = async () => {};", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-build", map[string]any{"status": "running"})
				assert.Expect(err).NotTo(HaveOccurred())

				router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
				assert.Expect(err).NotTo(HaveOccurred())

				req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID+"/tasks-partial/", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK))
				doc := mustHTMLDocument(t, rec)
				assert.Expect(doc.Find(`#run-error-alert[hx-swap-oob="true"]`).Length()).To(BeNumerically(">", 0))
				assert.Expect(doc.Find("[role='alert']").Length()).To(Equal(0))
			})

		})
	})
}
