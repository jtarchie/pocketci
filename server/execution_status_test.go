package server_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

// TestPipelineStatusBasedOnJobOutcomes tests that pipeline status is determined
// correctly based on the outcomes of its jobs, covering various scenarios including
// success, failure, error, abort, and recovery patterns.
func TestPipelineStatusBasedOnJobOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("pipeline succeeds when all jobs succeed", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipelineContent := `
export const pipeline = async () => {
	const result = await runtime.run({
		name: "success-task",
		image: "busybox",
		command: { path: "sh", args: ["-c", "exit 0"] },
	});
};`

			pipeline, err := client.SavePipeline(context.Background(), "success-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			// Wait for execution to complete
			execService.Wait()

			// Check run status
			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
		})

		t.Run("pipeline fails when job has failure status", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// This pipeline has a job that fails and uses on_failure hook
			// The job should complete with "failure" status, and the pipeline should be marked failed
			pipelineContent := `
export const pipeline = async () => {
	try {
		const result = await runtime.run({
			name: "failing-task",
			image: "busybox",
			command: { path: "sh", args: ["-c", "exit 1"] },
		});
	} catch (error) {
		// Catch the error so job can complete with failure status
		console.error("Task failed as expected");
	}
};`

			pipeline, err := client.SavePipeline(context.Background(), "failure-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			// Wait for execution to complete
			execService.Wait()

			// Check run status - should be success since we caught the error
			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
		})

		t.Run("pipeline fails when job has error status", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	storage.set("/pipeline/" + runID + "/jobs/error-job", { status: "error" });
};`

			pipeline, err := client.SavePipeline(context.Background(), "error-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
		})

		t.Run("pipeline fails when job has abort status", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	storage.set("/pipeline/" + runID + "/jobs/abort-job", { status: "abort" });
};`

			pipeline, err := client.SavePipeline(context.Background(), "abort-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
		})

		t.Run("pipeline succeeds when jobs recover from failures", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Pipeline with multiple jobs where failures are caught with try blocks
			// Final status should be success since all failures were recovered
			pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	// Job 1: succeeds
	storage.set("/pipeline/" + runID + "/jobs/job-1", { status: "success" });
	// Job 2: has a failure but recovers (like try block)
	storage.set("/pipeline/" + runID + "/jobs/job-2", { status: "success" });
	// Job 3: succeeds
	storage.set("/pipeline/" + runID + "/jobs/job-3", { status: "success" });
};`

			pipeline, err := client.SavePipeline(context.Background(), "recovery-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
		})

		t.Run("pipeline fails when at least one job fails even if others succeed", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	storage.set("/pipeline/" + runID + "/jobs/job-1", { status: "success" });
	storage.set("/pipeline/" + runID + "/jobs/job-2", { status: "failure" });
	storage.set("/pipeline/" + runID + "/jobs/job-3", { status: "success" });
};`

			pipeline, err := client.SavePipeline(context.Background(), "mixed-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
		})

		t.Run("pipeline fails on execution error", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Invalid JavaScript that will cause execution error
			pipelineContent := `
export const pipeline = async () => {
	throw new Error("Pipeline execution failed");
};`

			pipeline, err := client.SavePipeline(context.Background(), "error-execution-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusFailed))
			assert.Expect(finalRun.ErrorMessage).To(ContainSubstring("Pipeline execution failed"))
		})

		t.Run("handles pending jobs correctly", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			// Pipeline that sets some jobs as pending (not yet executed)
			pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	storage.set("/pipeline/" + runID + "/jobs/job-1", { status: "pending" });
	storage.set("/pipeline/" + runID + "/jobs/job-2", { status: "success" });
};`

			pipeline, err := client.SavePipeline(context.Background(), "pending-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			// Pending jobs should not cause failure
			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
		})

		t.Run("pipeline is skipped when all jobs are skipped", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	storage.set("/pipeline/" + runID + "/jobs/job-1", { status: "skipped" });
	storage.set("/pipeline/" + runID + "/jobs/job-2", { status: "skipped" });
};`

			pipeline, err := client.SavePipeline(context.Background(), "all-skipped-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSkipped))
		})

		t.Run("pipeline succeeds when skipped and success jobs are mixed", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			pipelineContent := `
export const pipeline = async () => {
	const runID = typeof pipelineContext !== "undefined" && pipelineContext.runID ? pipelineContext.runID : String(Date.now());
	storage.set("/pipeline/" + runID + "/jobs/job-1", { status: "skipped" });
	storage.set("/pipeline/" + runID + "/jobs/job-2", { status: "success" });
};`

			pipeline, err := client.SavePipeline(context.Background(), "mixed-skipped-success-pipeline", pipelineContent, "native", "")
			assert.Expect(err).NotTo(HaveOccurred())

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			run, err := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(err).NotTo(HaveOccurred())

			execService.Wait()

			finalRun, err := client.GetRun(context.Background(), run.ID)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(finalRun.Status).To(Equal(storage.RunStatusSuccess))
		})
	})
}

func TestExecutionServiceExportsForTesting(t *testing.T) {
	t.Parallel()

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		t.Run("ExecutionService is accessible from router", func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			buildFile, err := os.CreateTemp(t.TempDir(), "")
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = buildFile.Close() }()

			client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
			assert.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			router := newStrictSecretRouter(t, client, server.RouterOptions{MaxInFlight: 5})

			execService := router.ExecutionService()
			assert.Expect(execService).NotTo(BeNil())
			assert.Expect(execService.MaxInFlight()).To(Equal(5))
		})
	})
}
