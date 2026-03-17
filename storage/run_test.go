package storage_test

import (
	"context"
	"testing"

	"github.com/jtarchie/pocketci/storage"
	. "github.com/onsi/gomega"
)

func TestPipelineRunStorage(t *testing.T) {
	for _, df := range allDrivers() {
		t.Run(df.name, func(t *testing.T) {
			t.Run("SaveRun creates a new run with queued status", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "console.log('hello');", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(run).NotTo(BeNil())
				assert.Expect(run.ID).NotTo(BeEmpty())
				assert.Expect(run.PipelineID).To(Equal(pipeline.ID))
				assert.Expect(run.Status).To(Equal(storage.RunStatusQueued))
				assert.Expect(run.StartedAt).To(BeNil())
				assert.Expect(run.CompletedAt).To(BeNil())
				assert.Expect(run.ErrorMessage).To(BeEmpty())
				assert.Expect(run.CreatedAt).NotTo(BeZero())
			})

			t.Run("GetRun retrieves existing run", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				pipeline, err := client.SavePipeline(context.Background(), "my-pipeline", "export { pipeline };", "native://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				saved, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				retrieved, err := client.GetRun(context.Background(), saved.ID)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(retrieved.ID).To(Equal(saved.ID))
				assert.Expect(retrieved.PipelineID).To(Equal(pipeline.ID))
				assert.Expect(retrieved.Status).To(Equal(storage.RunStatusQueued))
			})

			t.Run("GetRun returns error for non-existent ID", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				_, err := client.GetRun(context.Background(), "non-existent-id")
				assert.Expect(err).To(Equal(storage.ErrNotFound))
			})

			t.Run("UpdateRunStatus to running sets started_at", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				pipeline, err := client.SavePipeline(context.Background(), "pipeline", "content", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
				assert.Expect(err).NotTo(HaveOccurred())

				updated, err := client.GetRun(context.Background(), run.ID)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(updated.Status).To(Equal(storage.RunStatusRunning))
				assert.Expect(updated.StartedAt).NotTo(BeNil())
				assert.Expect(updated.CompletedAt).To(BeNil())
			})

			t.Run("UpdateRunStatus to success sets completed_at", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				pipeline, err := client.SavePipeline(context.Background(), "pipeline", "content", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSuccess, "")
				assert.Expect(err).NotTo(HaveOccurred())

				updated, err := client.GetRun(context.Background(), run.ID)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(updated.Status).To(Equal(storage.RunStatusSuccess))
				assert.Expect(updated.CompletedAt).NotTo(BeNil())
				assert.Expect(updated.ErrorMessage).To(BeEmpty())
			})

			t.Run("UpdateRunStatus to failed sets error_message", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				pipeline, err := client.SavePipeline(context.Background(), "pipeline", "content", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusFailed, "something went wrong")
				assert.Expect(err).NotTo(HaveOccurred())

				updated, err := client.GetRun(context.Background(), run.ID)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(updated.Status).To(Equal(storage.RunStatusFailed))
				assert.Expect(updated.CompletedAt).NotTo(BeNil())
				assert.Expect(updated.ErrorMessage).To(Equal("something went wrong"))
			})

			t.Run("UpdateRunStatus to skipped sets completed_at", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				pipeline, err := client.SavePipeline(context.Background(), "pipeline", "content", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(context.Background(), pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusRunning, "")
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateRunStatus(context.Background(), run.ID, storage.RunStatusSkipped, "")
				assert.Expect(err).NotTo(HaveOccurred())

				updated, err := client.GetRun(context.Background(), run.ID)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(updated.Status).To(Equal(storage.RunStatusSkipped))
				assert.Expect(updated.CompletedAt).NotTo(BeNil())
				assert.Expect(updated.ErrorMessage).To(BeEmpty())
			})

			t.Run("UpdateRunStatus returns error for non-existent ID", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				err := client.UpdateRunStatus(context.Background(), "non-existent-id", storage.RunStatusRunning, "")
				assert.Expect(err).To(Equal(storage.ErrNotFound))
			})

			t.Run("SearchRunsByPipeline", func(t *testing.T) {
				t.Run("empty query returns all runs for pipeline", func(t *testing.T) {
					assert := NewGomegaWithT(t)

					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(context.Background(), "search-test", "content", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())

					_, err = client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					_, err = client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())

					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(2))
				})

				t.Run("filters by status", func(t *testing.T) {
					assert := NewGomegaWithT(t)

					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(context.Background(), "status-pipeline", "content", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())

					run1, err := client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusSuccess, "")
					assert.Expect(err).NotTo(HaveOccurred())

					_, err = client.SaveRun(context.Background(), pipeline.ID) // queued
					assert.Expect(err).NotTo(HaveOccurred())

					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "success", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].Status).To(Equal(storage.RunStatusSuccess))
				})

				t.Run("filters by error message", func(t *testing.T) {
					assert := NewGomegaWithT(t)

					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(context.Background(), "err-pipeline", "content", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())

					run1, err := client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusFailed, "timeout exceeded")
					assert.Expect(err).NotTo(HaveOccurred())

					_, err = client.SaveRun(context.Background(), pipeline.ID) // queued, no error
					assert.Expect(err).NotTo(HaveOccurred())

					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "timeout", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].ErrorMessage).To(ContainSubstring("timeout"))
				})

				t.Run("returns empty when query matches nothing", func(t *testing.T) {
					assert := NewGomegaWithT(t)

					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(context.Background(), "nomatch-pipeline", "content", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())

					_, err = client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())

					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "zzznomatch", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(BeEmpty())
				})

				t.Run("is scoped to the requested pipeline", func(t *testing.T) {
					assert := NewGomegaWithT(t)

					client := df.new(t, "namespace")

					pipA, err := client.SavePipeline(context.Background(), "pipeline-a", "content", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					pipB, err := client.SavePipeline(context.Background(), "pipeline-b", "other", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())

					runA, err := client.SaveRun(context.Background(), pipA.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), runA.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), runA.ID, storage.RunStatusFailed, "unique-error-alpha")
					assert.Expect(err).NotTo(HaveOccurred())

					runB, err := client.SaveRun(context.Background(), pipB.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), runB.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), runB.ID, storage.RunStatusFailed, "unique-error-alpha")
					assert.Expect(err).NotTo(HaveOccurred())

					// Searching pipeline A should only return pipeline A's run
					result, err := client.SearchRunsByPipeline(context.Background(), pipA.ID, "alpha", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].PipelineID).To(Equal(pipA.ID))
				})
			})
		})
	}
}
