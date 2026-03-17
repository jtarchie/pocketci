package storage_test

import (
	"context"
	"testing"

	"github.com/jtarchie/pocketci/storage"
	. "github.com/onsi/gomega"
)

func TestPipelineStorage(t *testing.T) {
	for _, df := range allDrivers() {
		t.Run(df.name, func(t *testing.T) {
			t.Run("SavePipeline creates a new pipeline", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				pipeline, err := client.SavePipeline(context.Background(), "test-pipeline", "console.log('hello');", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(pipeline).NotTo(BeNil())
				assert.Expect(pipeline.ID).NotTo(BeEmpty())
				assert.Expect(pipeline.Name).To(Equal("test-pipeline"))
				assert.Expect(pipeline.Content).To(Equal("console.log('hello');"))
				assert.Expect(pipeline.DriverDSN).To(Equal("docker://"))
				assert.Expect(pipeline.CreatedAt).NotTo(BeZero())
				assert.Expect(pipeline.UpdatedAt).NotTo(BeZero())
			})

			t.Run("GetPipeline retrieves existing pipeline", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				saved, err := client.SavePipeline(context.Background(), "my-pipeline", "export { pipeline };", "native://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				retrieved, err := client.GetPipeline(context.Background(), saved.ID)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(retrieved.ID).To(Equal(saved.ID))
				assert.Expect(retrieved.Name).To(Equal("my-pipeline"))
				assert.Expect(retrieved.Content).To(Equal("export { pipeline };"))
				assert.Expect(retrieved.DriverDSN).To(Equal("native://"))
			})

			t.Run("GetPipeline returns error for non-existent ID", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				_, err := client.GetPipeline(context.Background(), "non-existent-id")
				assert.Expect(err).To(Equal(storage.ErrNotFound))
			})

			t.Run("ListPipelines returns all pipelines", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				_, err := client.SavePipeline(context.Background(), "pipeline-1", "content1", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				_, err = client.SavePipeline(context.Background(), "pipeline-2", "content2", "native://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				result, err := client.SearchPipelines(context.Background(), "", 1, 100)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(result.Items).To(HaveLen(2))
			})

			t.Run("ListPipelines returns empty slice when no pipelines", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				result, err := client.SearchPipelines(context.Background(), "", 1, 100)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(result.Items).To(BeEmpty())
			})

			t.Run("DeletePipeline removes a pipeline", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				saved, err := client.SavePipeline(context.Background(), "to-delete", "content", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.DeletePipeline(context.Background(), saved.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				_, err = client.GetPipeline(context.Background(), saved.ID)
				assert.Expect(err).To(Equal(storage.ErrNotFound))
			})

			t.Run("DeletePipeline returns error for non-existent ID", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				err := client.DeletePipeline(context.Background(), "non-existent-id")
				assert.Expect(err).To(Equal(storage.ErrNotFound))
			})

			t.Run("DeletePipeline cascades to runs and task data", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				if df.name == "s3" {
					t.Skip("S3 driver does not cascade-delete task key/value records on pipeline deletion")
				}

				client := df.new(t, "namespace")

				ctx := context.Background()

				pipeline, err := client.SavePipeline(ctx, "cascade-test", "export { pipeline };", "native://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				run, err := client.SaveRun(ctx, pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				taskPath := "/pipeline/" + run.ID + "/tasks/0-echo-task"
				err = client.Set(ctx, taskPath, map[string]string{"status": "running"})
				assert.Expect(err).NotTo(HaveOccurred())

				// Verify task data exists before deletion
				results, err := client.GetAll(ctx, "/pipeline/"+run.ID+"/", []string{"status"})
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(results).NotTo(BeEmpty())

				// Delete the pipeline; runs and task data should cascade-delete
				err = client.DeletePipeline(ctx, pipeline.ID)
				assert.Expect(err).NotTo(HaveOccurred())

				// Run should be gone
				_, err = client.GetRun(ctx, run.ID)
				assert.Expect(err).To(Equal(storage.ErrNotFound))

				// Task data should be gone
				results, err = client.GetAll(ctx, "/pipeline/"+run.ID+"/", []string{"status"})
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(results).To(BeEmpty())
			})

			t.Run("GetPipelineByName returns the most recent pipeline with that name", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				ctx := context.Background()

				saved, err := client.SavePipeline(ctx, "k6", "export { pipeline };", "native://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				retrieved, err := client.GetPipelineByName(ctx, "k6")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(retrieved.ID).To(Equal(saved.ID))
				assert.Expect(retrieved.Name).To(Equal("k6"))
			})

			t.Run("GetPipelineByName returns ErrNotFound for unknown name", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				_, err := client.GetPipelineByName(context.Background(), "nonexistent")
				assert.Expect(err).To(Equal(storage.ErrNotFound))
			})

			t.Run("SavePipeline called twice with same name updates content instead of creating a second pipeline", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				ctx := context.Background()

				first, err := client.SavePipeline(ctx, "my-pipeline", "content-v1", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				second, err := client.SavePipeline(ctx, "my-pipeline", "content-v2", "docker://", "")
				assert.Expect(err).NotTo(HaveOccurred())

				// The stable ID must not change across updates.
				assert.Expect(second.ID).To(Equal(first.ID))

				// Content must reflect the second call.
				assert.Expect(second.Content).To(Equal("content-v2"))

				// Only one pipeline should exist in the database.
				result, err := client.SearchPipelines(ctx, "", 1, 100)
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(result.Items).To(HaveLen(1))
				assert.Expect(result.Items[0].Name).To(Equal("my-pipeline"))
				assert.Expect(result.Items[0].Content).To(Equal("content-v2"))
			})
		})
	}
}
