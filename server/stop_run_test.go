package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestStopRun(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("POST /api/runs/:run_id/stop returns 404 for non-existent run", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newStrictSecretRouter(t, client, server.RouterOptions{})

			req := httptest.NewRequest(http.MethodPost, "/api/runs/does-not-exist/stop", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusNotFound))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).NotTo(BeEmpty())
		})

		t.Run("POST /api/runs/:run_id/stop returns 409 for already completed run", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipeline, err := client.SavePipeline(context.Background(), "stop-test-pipeline",
				"export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			// Wait for the pipeline to finish so the run is in a terminal state
			execService.Wait()

			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/stop", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusConflict))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["error"]).NotTo(BeEmpty())
		})

		t.Run("POST /api/runs/:run_id/stop cancels an in-flight run and marks it failed", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// A pipeline that sleeps so it's still running when we call stop
			pipelineContent := `
export const pipeline = async () => {
	await runtime.run({
		name: "long-task",
		image: "busybox",
		command: { path: "sh", args: ["-c", "sleep 60"] },
	});
};`
			pipeline, err := client.SavePipeline(context.Background(), "stop-inflight-pipeline",
				pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			// Poll until the run is in running state before stopping it
			assert.Eventually(func() bool {
				r, rErr := client.GetRun(context.Background(), run.ID)
				return rErr == nil && r.Status == storage.RunStatusRunning
			}, 10*time.Second, 100*time.Millisecond)

			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/stop", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["run_id"]).To(Equal(run.ID))
			// Both "stopping" and "stopped" are valid: "stopping" means the run was actively
			// in-flight and cancelled asynchronously; "stopped" means the run completed just
			// before the stop was processed (race window). The definitive check is below.
			assert.Expect(resp["status"]).To(Or(Equal("stopping"), Equal("stopped")))

			// Wait for the goroutine to fully exit
			execService.Wait()

			// Verify the run was marked as failed with our stop message
			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
			assert.Expect(finalRun.ErrorMessage).To(Equal("Run stopped by user"))
		})

		t.Run("POST /api/runs/:run_id/stop repeatedly keeps final status failed", func(t *testing.T) {
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipelineContent := `
export const pipeline = async () => {
	await runtime.run({
		name: "long-task",
		image: "busybox",
		command: { path: "sh", args: ["-c", "sleep 15"] },
	});
};`
			pipeline, err := client.SavePipeline(context.Background(), "stop-repeated-pipeline",
				pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})
			execService := router.ExecutionService()

			for i := range 5 {
				run, triggerErr := execService.TriggerPipeline(context.Background(), pipeline, nil)
				assert.Expect(triggerErr).NotTo(HaveOccurred(), "iteration %d trigger", i)

				assert.Eventually(func() bool {
					r, rErr := client.GetRun(context.Background(), run.ID)
					return rErr == nil && r.Status == storage.RunStatusRunning
				}, 10*time.Second, 100*time.Millisecond, "iteration %d wait running", i)

				req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/stop", nil)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				assert.Expect(rec.Code).To(Equal(http.StatusOK), "iteration %d stop response", i)

				var resp map[string]string
				unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp)
				assert.Expect(unmarshalErr).NotTo(HaveOccurred(), "iteration %d stop payload", i)
				assert.Expect(resp["status"]).To(Or(Equal("stopping"), Equal("stopped")), "iteration %d stop status", i)

				execService.Wait()

				finalRun, finalErr := client.GetRun(context.Background(), run.ID)
				assert.Expect(finalErr).NotTo(HaveOccurred(), "iteration %d final run", i)
				assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed), "iteration %d final status", i)
				assert.Expect(finalRun.ErrorMessage).To(Equal("Run stopped by user"), "iteration %d final message", i)
			}
		})

		t.Run("StopRun returns ErrRunNotInFlight when run is not in registry", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newStrictSecretRouter(t, client, server.RouterOptions{})

			err = router.ExecutionService().StopRun("unknown-run-id")
			assert.Expect(err).To(MatchError(server.ErrRunNotInFlight))
		})

		t.Run("POST /api/runs/:run_id/stop clears orphaned run stuck in running state", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)

			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Create router first so that RecoverOrphanedRuns runs before we create the orphan.
			router := newStrictSecretRouter(t, client, server.RouterOptions{})

			// Simulate a run that was left in "running" state (e.g. after a server crash)
			// by saving a run then manually forcing its status to running without a live goroutine.
			pipeline, err := client.SavePipeline(context.Background(), "orphan-pipeline",
				"export const pipeline = async () => {};", "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
			assert.Expect(err).NotTo(HaveOccurred())

			err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
			assert.Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/stop", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Expect(rec.Code).To(Equal(http.StatusOK))

			var resp map[string]string
			err = json.Unmarshal(rec.Body.Bytes(), &resp)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(resp["run_id"]).To(Equal(run.ID))
			assert.Expect(resp["status"]).To(Equal("stopped"))

			// Verify the orphaned run was forced to failed
			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
			assert.Expect(finalRun.ErrorMessage).To(Equal("Run stopped by user"))
		})
	})
}
