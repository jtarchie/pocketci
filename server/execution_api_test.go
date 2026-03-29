package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestExecutionAPI(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("POST /api/pipelines/:id/trigger returns run_id", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a simple pipeline that will fail quickly (no actual execution in test)
			pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})
			persistPipelineDriverSecret(t, router.ExecutionService().SecretsManager, pipeline.ID, "native")

			req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/trigger", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["run_id"]).NotTo(BeEmpty())
			assert.Expect(resp["pipeline_id"]).To(Equal(pipeline.ID))
			assert.Expect(resp["status"]).To(Equal("queued"))
			assert.Expect(resp["message"]).To(Equal("pipeline execution started"))

			// Wait for background goroutines to complete before cleanup
			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("POST /api/pipelines/:id/trigger with args mode passes args to execution", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(), "test-args-pipeline", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})
			persistPipelineDriverSecret(t, router.ExecutionService().SecretsManager, pipeline.ID, "native")

			body, _ := json.Marshal(map[string]any{
				"mode": "args",
				"args": []string{"--env=staging", "--verbose"},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/trigger", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["run_id"]).NotTo(BeEmpty())
			assert.Expect(resp["status"]).To(Equal("queued"))

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("POST /api/pipelines/:id/trigger with webhook mode triggers webhook execution", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			pipeline, err := client.SavePipeline(context.Background(), "test-webhook-pipeline", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:     5,
				AllowedFeatures: "webhooks",
			})
			persistPipelineDriverSecret(t, router.ExecutionService().SecretsManager, pipeline.ID, "native")

			body, _ := json.Marshal(map[string]any{
				"mode": "webhook",
				"webhook": map[string]any{
					"method": "POST",
					"body":   `{"action":"opened"}`,
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/trigger", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["run_id"]).NotTo(BeEmpty())

			router.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("POST /api/pipelines/:id/trigger with webhook mode returns 403 when webhooks disabled", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(), "test-no-webhook", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:     5,
				AllowedFeatures: "secrets",
			})

			body, _ := json.Marshal(map[string]any{
				"mode": "webhook",
				"webhook": map[string]any{
					"method": "POST",
					"body":   "{}",
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/trigger", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusForbidden))
		})

		t.Run("POST /api/pipelines/:id/trigger returns 404 for non-existent pipeline", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodPost, "/api/pipelines/non-existent/trigger", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
		})

		t.Run("GET /api/runs/:run_id/status returns run details", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Create pipeline and run directly
			pipeline, err := client.SavePipeline(context.Background(), "my-pipeline", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
			assert.Expect(err).NotTo(HaveOccurred())

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/status", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp storage.PipelineRun
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp.ID).To(Equal(run.ID))
			assert.Expect(resp.PipelineID).To(Equal(pipeline.ID))
			assert.Expect(resp.Status).To(Equal(storage.RunStatusQueued))
		})

		t.Run("GET /api/runs/:run_id/status returns 404 for non-existent run", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodGet, "/api/runs/non-existent/status", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
		})

		t.Run("GET /api/runs/:run_id/tasks returns run tasks payload", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(), "agent-pipeline", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
			assert.Expect(err).NotTo(HaveOccurred())

			err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-review", map[string]any{
				"status": "success",
				"stdout": "done",
				"usage": map[string]any{
					"totalTokens": 1200,
				},
				"audit_log": []any{map[string]any{"event": "tool_call"}},
			})
			assert.Expect(err).NotTo(HaveOccurred())

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/tasks", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp []map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp).NotTo(BeEmpty())
			assert.Expect(resp[0]["path"]).To(Equal("/pipeline/" + run.ID + "/tasks/0-review"))

			payload, ok := resp[0]["payload"].(map[string]any)
			assert.Expect(ok).To(BeTrue())
			assert.Expect(payload["status"]).To(Equal("success"))
			assert.Expect(payload["usage"]).NotTo(BeNil())
			assert.Expect(payload["audit_log"]).NotTo(BeNil())
		})

		t.Run("GET /api/runs/:run_id/tasks supports task path filter", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(), "filtered-pipeline", "content", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
			assert.Expect(err).NotTo(HaveOccurred())

			err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/0-a", map[string]any{"status": "success"})
			assert.Expect(err).NotTo(HaveOccurred())
			err = client.Set(context.Background(), "/pipeline/"+run.ID+"/tasks/1-b", map[string]any{"status": "failure"})
			assert.Expect(err).NotTo(HaveOccurred())

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID+"/tasks?path=/pipeline/"+run.ID+"/tasks/1-b", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp []map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp).To(HaveLen(1))
			assert.Expect(resp[0]["path"]).To(Equal("/pipeline/" + run.ID + "/tasks/1-b"))

			payload, ok := resp[0]["payload"].(map[string]any)
			assert.Expect(ok).To(BeTrue())
			assert.Expect(payload["status"]).To(Equal("failure"))
		})

		t.Run("GET /api/runs/:run_id/tasks returns 404 for non-existent run", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodGet, "/api/runs/non-existent/tasks", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
		})

		t.Run("POST /api/pipelines/:id/trigger returns 429 when max-in-flight reached", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			// Create multiple pipelines
			pipeline1, err := client.SavePipeline(context.Background(), "pipeline-1", "export const pipeline = async () => { console.log('pipeline 1'); };", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())
			pipeline2, err := client.SavePipeline(context.Background(), "pipeline-2", "export const pipeline = async () => { console.log('pipeline 2'); };", "docker", "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Set max-in-flight to 0 - should reject all new executions
			_ = newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 0})

			// Even the first trigger should fail because max-in-flight defaults to 10 when 0
			// So let's test differently - with max 1, trigger twice quickly before goroutine starts

			// Actually, let's just verify the endpoint returns 429 properly
			// by checking the canExecute logic works
			// We'll trigger once then immediately trigger again before goroutine gets far

			router2 := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 1})
			persistPipelineDriverSecret(t, router2.ExecutionService().SecretsManager, pipeline1.ID, "docker")
			persistPipelineDriverSecret(t, router2.ExecutionService().SecretsManager, pipeline2.ID, "docker")

			// Make first request
			req1 := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline1.ID+"/trigger", nil)
			rec1 := httptest.NewRecorder()
			router2.ServeHTTP(rec1, req1)
			assert.Expect(rec1.Code).To(Equal(http.StatusAccepted))

			// Immediately make second request - this should work because execution
			// happens in goroutine and canExecute check happens before increment
			// Actually, the increment happens before goroutine is launched, so this should return 429
			req2 := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline2.ID+"/trigger", nil)
			rec2 := httptest.NewRecorder()
			router2.ServeHTTP(rec2, req2)

			// Due to the mutex lock, the second request should see the incremented counter
			assert.Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))

			var resp map[string]any
			err = json.Unmarshal(rec2.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).To(Equal("execution queue is full"))

			// Wait for background goroutines to complete before cleanup
			router2.WaitForExecutions()
			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("POST /api/pipelines/:name/run returns SSE exit event", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(), "my-pipeline", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})
			persistPipelineDriverSecret(t, router.ExecutionService().SecretsManager, pipeline.ID, "native")

			req := httptest.NewRequest(http.MethodPost, "/api/pipelines/my-pipeline/run", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))
			assert.Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/event-stream"))
			events := mustSSEJSONEvents(t, rec)
			exitEvent := events[len(events)-1]
			assert.Expect(exitEvent["event"]).To(Equal("exit"))
			assert.Expect(exitEvent["code"]).To(Equal(float64(0)))
		})

		t.Run("POST /api/pipelines/:name/run returns error event for unknown pipeline", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router, err := server.NewRouter(slog.Default(), client, server.RouterOptions{})
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodPost, "/api/pipelines/nonexistent/run", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
			errPayload := mustJSONMap(t, rec)
			assert.Expect(errPayload).To(HaveKey("error"))
		})
	})
}
