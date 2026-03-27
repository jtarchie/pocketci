package storage_test

import (
	"context"
	"testing"

	"github.com/jtarchie/pocketci/storage"
	. "github.com/onsi/gomega"
)

func TestGetMostRecentJobStatus(t *testing.T) {
	for _, df := range allDrivers() {
		t.Run(df.name, func(t *testing.T) {
			t.Run("returns empty string when no records exist", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "test-ns")

				status, err := client.GetMostRecentJobStatus(context.Background(), "any-pipeline", "build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal(""))
			})

			t.Run("returns success when most recent record is success (CLI mode)", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "cli-ns")

				err := client.Set(context.Background(), "/pipeline/run-1/jobs/build", map[string]any{
					"status": "success",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				status, err := client.GetMostRecentJobStatus(context.Background(), "", "build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal("success"))
			})

			t.Run("returns failed when most recent run failed even if earlier succeeded (CLI mode)", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "cli-ns-2")

				// Use IDs that sort alphabetically: run-aaa < run-zzz
				err := client.Set(context.Background(), "/pipeline/run-aaa/jobs/build", map[string]any{
					"status": "success",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/run-zzz/jobs/build", map[string]any{
					"status": "failed",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				status, err := client.GetMostRecentJobStatus(context.Background(), "", "build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal("failed"))
			})

			t.Run("ignores skipped and pending records", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "cli-ns-3")

				err := client.Set(context.Background(), "/pipeline/run-1/jobs/deploy", map[string]any{
					"status": "success",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/run-2/jobs/deploy", map[string]any{
					"status": "skipped",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/pipeline/run-3/jobs/deploy", map[string]any{
					"status": "pending",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				status, err := client.GetMostRecentJobStatus(context.Background(), "", "deploy")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal("success"))
			})

			t.Run("job names with LIKE wildcards do not match other jobs", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "cli-ns-4")

				// Set up a job with a name that would match LIKE wildcards
				err := client.Set(context.Background(), "/pipeline/run-1/jobs/build-image", map[string]any{
					"status": "success",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				// Query with a name containing % — should NOT match build-image
				status, err := client.GetMostRecentJobStatus(context.Background(), "", "build%")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal(""))

				// Query with a name containing _ — should NOT match build-image
				status, err = client.GetMostRecentJobStatus(context.Background(), "", "build_image")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal(""))
			})

			t.Run("scopes by pipeline in server mode (empty namespace)", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "")

				// Create two pipelines
				p1, err := client.SavePipeline(context.Background(), "pipeline-a", "content-a", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				p2, err := client.SavePipeline(context.Background(), "pipeline-b", "content-b", "docker", "")
				assert.Expect(err).NotTo(HaveOccurred())

				// Create runs for each pipeline
				run1, err := client.SaveRun(context.Background(), p1.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
				assert.Expect(err).NotTo(HaveOccurred())

				run2, err := client.SaveRun(context.Background(), p2.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
				assert.Expect(err).NotTo(HaveOccurred())

				// Write job status for pipeline A's run
				err = client.Set(context.Background(), "/pipeline/"+run1.ID+"/jobs/build", map[string]any{
					"status": "success",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				// Write job status for pipeline B's run
				err = client.Set(context.Background(), "/pipeline/"+run2.ID+"/jobs/build", map[string]any{
					"status": "failed",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				// Pipeline A should see success
				status, err := client.GetMostRecentJobStatus(context.Background(), p1.ID, "build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal("success"))

				// Pipeline B should see failed
				status, err = client.GetMostRecentJobStatus(context.Background(), p2.ID, "build")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(status).To(Equal("failed"))
			})
		})
	}
}
