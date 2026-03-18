package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	_ "github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestResumeAPI(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("POST /api/runs/:run_id/resume resumes a failed run", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a pipeline that fails
			pipelineContent := `
export const pipeline = async () => {
	throw new Error("intentional failure");
};`
			pipeline, err := client.SavePipeline(context.Background(), "resume-test", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			// Trigger the pipeline and wait for it to fail
			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline)
			assert.Expect(err).NotTo(HaveOccurred())
			execService.Wait()

			// Verify it failed
			failedRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(failedRun.Status).To(Equal(storage.RunStatusFailed))

			// Resume via API
			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/resume", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["run_id"]).To(Equal(run.ID))
			assert.Expect(resp["status"]).To(Equal("resuming"))

			// Wait for resumed execution to finish
			router.WaitForExecutions()

			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("POST /api/runs/:run_id/resume returns 400 for non-failed run", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Create a pipeline that succeeds
			pipelineContent := `
export const pipeline = async () => {
	const result = await runtime.run({
		name: "ok",
		image: "busybox",
		command: { path: "sh", args: ["-c", "exit 0"] },
	});
};`
			pipeline, err := client.SavePipeline(context.Background(), "resume-test-success", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline)
			assert.Expect(err).NotTo(HaveOccurred())
			execService.Wait()

			// Verify it succeeded
			successRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(successRun.Status).To(Equal(storage.RunStatusSuccess))

			// Try to resume - should fail
			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/resume", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusConflict))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).To(ContainSubstring("only failed runs can be resumed"))
		})

		t.Run("POST /api/runs/:run_id/resume returns 404 for non-existent run", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			req := httptest.NewRequest(http.MethodPost, "/api/runs/nonexistent/resume", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))
		})
	})
}

func TestOrphanRecovery(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("orphaned runs are marked failed when auto-resume is off", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Create a pipeline
			pipeline, err := client.SavePipeline(context.Background(), "orphan-test", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a run and set its status to "running" to simulate a crash
			run, err := client.SaveRun(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a new router with resume feature disabled — orphans should be marked failed
			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight:     5,
				AllowedFeatures: "webhooks,secrets,notifications,fetch",
			})

			router.WaitForExecutions()

			// Verify the run was marked as failed
			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
		})

		t.Run("orphaned runs are resumed when auto-resume is on and pipeline has resume_enabled", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a pipeline with resume_enabled
			pipelineContent := `
export const pipeline = async () => {
	const result = await runtime.run({
		name: "recovered-task",
		image: "busybox",
		command: { path: "sh", args: ["-c", "exit 0"] },
	});
};`
			pipeline, err := client.SavePipeline(context.Background(), "orphan-resume-test", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Enable resume on the pipeline
			err = client.UpdatePipelineResumeEnabled(context.Background(), pipeline.ID, true)
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a run and set its status to "running" to simulate a crash
			run, err := client.SaveRun(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Create router with resume feature enabled — should auto-resume the orphan
			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight: 5,
			})

			// Wait for resumed execution to complete
			router.WaitForExecutions()

			// Verify the run completed (it was resumed)
			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			// The resumed pipeline should succeed since the JS just does a simple echo
			assert.Expect(finalRun.Status).NotTo(Equal(storage.RunStatusRunning))

			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("orphaned runs stay failed when auto-resume is on but pipeline has resume_enabled=false", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Create a pipeline WITHOUT resume_enabled
			pipeline, err := client.SavePipeline(context.Background(), "orphan-no-resume", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a run and set its status to "running" to simulate a crash
			run, err := client.SaveRun(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Create a new router with resume feature enabled, but pipeline doesn't have resume_enabled
			router := newStrictSecretRouter(t, client, server.RouterOptions{
				MaxInFlight: 5,
			})

			router.WaitForExecutions()

			// Should still be marked failed since pipeline doesn't have resume_enabled
			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
		})
	})
}

func TestResumeEnabled(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("pipeline resume_enabled persists through update", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Create pipeline - defaults to resume_enabled=false
			pipeline, err := client.SavePipeline(context.Background(), "resume-persist", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(pipeline.ResumeEnabled).To(BeFalse())

			// Enable resume
			err = client.UpdatePipelineResumeEnabled(context.Background(), pipeline.ID, true)
			assert.Expect(err).NotTo(HaveOccurred())

			// Re-fetch and verify
			updated, err := client.GetPipeline(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(updated.ResumeEnabled).To(BeTrue())

			// Disable resume
			err = client.UpdatePipelineResumeEnabled(context.Background(), pipeline.ID, false)
			assert.Expect(err).NotTo(HaveOccurred())

			disabled, err := client.GetPipeline(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(disabled.ResumeEnabled).To(BeFalse())
		})

		t.Run("GetRunsByStatus returns matching runs", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(), "status-test", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Create some runs with different statuses
			run1, err := client.SaveRun(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusRunning, "")
			assert.Expect(err).NotTo(HaveOccurred())

			run2, err := client.SaveRun(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			err = client.UpdateRunStatus(context.Background(), run2.ID, storage.RunStatusFailed, "")
			assert.Expect(err).NotTo(HaveOccurred())

			run3, err := client.SaveRun(context.Background(), pipeline.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			err = client.UpdateRunStatus(context.Background(), run3.ID, storage.RunStatusRunning, "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Query for running runs
			runningRuns, err := client.GetRunsByStatus(context.Background(), storage.RunStatusRunning)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(len(runningRuns)).To(Equal(2))

			// Query for failed runs
			failedRuns, err := client.GetRunsByStatus(context.Background(), storage.RunStatusFailed)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(len(failedRuns)).To(Equal(1))
			assert.Expect(failedRuns[0].ID).To(Equal(run2.ID))
		})
	})
}

func TestPipelineResumeFullIntegration(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("failed pipeline resumes and completes via server", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())

			// Pipeline that succeeds (we'll manually create a "failed" run for it and resume)
			pipelineContent := `
export const pipeline = async () => {
	const result = await runtime.run({
		name: "resume-step",
		image: "busybox",
		command: { path: "sh", args: ["-c", "echo hello && exit 0"] },
	});
};`
			pipeline, err := client.SavePipeline(context.Background(), "full-resume", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			// Enable resume
			err = client.UpdatePipelineResumeEnabled(context.Background(), pipeline.ID, true)
			assert.Expect(err).NotTo(HaveOccurred())

			// First: trigger and let it fail (simulate by running a failing pipeline first)
			failContent := `
export const pipeline = async () => {
	throw new Error("simulated failure");
};`
			failPipeline, err := client.SavePipeline(context.Background(), "fail-first", failContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			failRun, err := execService.TriggerPipeline(context.Background(), failPipeline)
			assert.Expect(err).NotTo(HaveOccurred())
			execService.Wait()

			// Verify it failed
			finalFail, err := client.GetRun(context.Background(), failRun.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalFail.Status).To(Equal(storage.RunStatusFailed))

			// Now resume through API
			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+failRun.ID+"/resume", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			router.WaitForExecutions()

			err = client.Close()
			assert.Expect(err).NotTo(HaveOccurred())
		})

		t.Run("resume_enabled reflected in pipeline API response", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(), "api-resume-test", "export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			err = client.UpdatePipelineResumeEnabled(context.Background(), pipeline.ID, true)
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			// GET /api/pipelines/:id should show resume_enabled
			req := httptest.NewRequest(http.MethodGet, "/api/pipelines/"+pipeline.ID, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp map[string]any
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["resume_enabled"]).To(BeTrue())
		})
	})
}
