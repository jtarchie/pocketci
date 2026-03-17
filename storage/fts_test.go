package storage_test

import (
	"context"
	"testing"

	"github.com/jtarchie/pocketci/storage"
	. "github.com/onsi/gomega"
)

func TestFTS(t *testing.T) {
	for _, df := range allDrivers() {
		t.Run(df.name, func(t *testing.T) {
			t.Run("SearchPipelines", func(t *testing.T) {
				t.Run("returns all pipelines when query is empty", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")

					_, err := client.SavePipeline(context.Background(), "hello-world", "echo hello", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchPipelines(context.Background(), "", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].Name).To(Equal("hello-world"))
				})

				t.Run("finds pipeline by name token", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")

					_, err := client.SavePipeline(context.Background(), "hello-world", "echo hello", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					_, err = client.SavePipeline(context.Background(), "deploy-prod", "kubectl apply", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchPipelines(context.Background(), "hello", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].Name).To(Equal("hello-world"))
				})

				t.Run("finds pipeline by content keyword", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					_, err := client.SavePipeline(context.Background(), "pipeline-a", "run unit tests with pytest", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					_, err = client.SavePipeline(context.Background(), "pipeline-b", "deploy to kubernetes", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchPipelines(context.Background(), "pytest", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].Name).To(Equal("pipeline-a"))
				})

				t.Run("returns empty when no pipeline matches", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					_, err := client.SavePipeline(context.Background(), "hello", "echo hi", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchPipelines(context.Background(), "zzznomatch", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(BeEmpty())
				})

				t.Run("prefix matching works for partial tokens", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					_, err := client.SavePipeline(context.Background(), "integration-tests", "run integration suite", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchPipelines(context.Background(), "integr", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
				})
			})

			t.Run("Set + Search", func(t *testing.T) {
				t.Run("finds task by stdout content", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					runID := "run-abc"
					taskKey := "/pipeline/" + runID + "/tasks/step-1"
					err := client.Set(context.Background(), taskKey, map[string]any{"stdout": "build passed successfully", "stderr": ""})
					assert.Expect(err).NotTo(HaveOccurred())
					results, err := client.Search(context.Background(), "pipeline/"+runID, "passed")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(HaveLen(1))
				})

				t.Run("finds task by stderr content", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					runID := "run-xyz"
					taskKey := "/pipeline/" + runID + "/tasks/step-1"
					err := client.Set(context.Background(), taskKey, map[string]any{"stdout": "", "stderr": "error: connection refused"})
					assert.Expect(err).NotTo(HaveOccurred())
					results, err := client.Search(context.Background(), "pipeline/"+runID, "connection")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(HaveLen(1))
				})

				t.Run("search is scoped to the requested run", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					run1 := "run-111"
					run2 := "run-222"
					err := client.Set(context.Background(), "/pipeline/"+run1+"/tasks/step-1", map[string]any{"stdout": "unique-token-alpha"})
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.Set(context.Background(), "/pipeline/"+run2+"/tasks/step-1", map[string]any{"stdout": "unique-token-alpha"})
					assert.Expect(err).NotTo(HaveOccurred())
					results, err := client.Search(context.Background(), "pipeline/"+run1, "alpha")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(HaveLen(1))
					for _, r := range results {
						assert.Expect(r.Path).To(ContainSubstring(run1))
					}
				})

				// ANSI stripping is performed by the SQLite FTS5 trigger — not applicable to S3.
				if df.name == "sqlite" {
					t.Run("ANSI codes are stripped so plain-text search succeeds", func(t *testing.T) {
						assert := NewGomegaWithT(t)
						client := df.new(t, "ns")
						runID := "run-ansi"
						taskKey := "/pipeline/" + runID + "/tasks/step-1"
						ansiOutput := "\x1b[32mBUILD_SUCCESS\x1b[0m: all tests passed"
						err := client.Set(context.Background(), taskKey, map[string]any{"stdout": ansiOutput, "stderr": ""})
						assert.Expect(err).NotTo(HaveOccurred())
						results, err := client.Search(context.Background(), "pipeline/"+runID, "BUILD_SUCCESS")
						assert.Expect(err).NotTo(HaveOccurred())
						assert.Expect(results).To(HaveLen(1))
						results, err = client.Search(context.Background(), "pipeline/"+runID, "\\x1b")
						assert.Expect(err).NotTo(HaveOccurred())
						assert.Expect(results).To(BeEmpty())
					})
				}

				t.Run("empty query returns nil results", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					results, err := client.Search(context.Background(), "pipeline/run-123", "")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(BeEmpty())
				})

				t.Run("re-indexing a task via Set replaces old entry", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					runID := "run-reindex"
					taskKey := "/pipeline/" + runID + "/tasks/step-1"
					err := client.Set(context.Background(), taskKey, map[string]any{"stdout": "first-run-output"})
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.Set(context.Background(), taskKey, map[string]any{"stdout": "second-run-output"})
					assert.Expect(err).NotTo(HaveOccurred())
					results, err := client.Search(context.Background(), "pipeline/"+runID, "first")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(BeEmpty())
					results, err = client.Search(context.Background(), "pipeline/"+runID, "second")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(HaveLen(1))
				})

				t.Run("path components are searchable", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					runID := "run-pathsearch"
					err := client.Set(context.Background(), "/pipeline/"+runID+"/tasks/compile-sources", map[string]any{"status": "success"})
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.Set(context.Background(), "/pipeline/"+runID+"/tasks/run-tests", map[string]any{"status": "success"})
					assert.Expect(err).NotTo(HaveOccurred())
					results, err := client.Search(context.Background(), "pipeline/"+runID, "compile")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(HaveLen(1))
					assert.Expect(results[0].Path).To(ContainSubstring("compile-sources"))
					results, err = client.Search(context.Background(), "pipeline/"+runID, "run-tests")
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(results).To(HaveLen(1))
					assert.Expect(results[0].Path).To(ContainSubstring("run-tests"))
				})
			})

			t.Run("SearchRunsByPipeline", func(t *testing.T) {
				t.Run("finds run by status", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					pipeline, err := client.SavePipeline(context.Background(), "my-pipe", "echo hi", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					run1, err := client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					_, err = client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusFailed, "out of memory")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "failed", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].ID).To(Equal(run1.ID))
				})

				t.Run("finds run by error message word", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					pipeline, err := client.SavePipeline(context.Background(), "my-pipe", "echo hi", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					run1, err := client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					_, err = client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusFailed, "segmentation-fault-unique-token")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "segmentation", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].ID).To(Equal(run1.ID))
				})

				t.Run("returns empty when no run matches", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					pipeline, err := client.SavePipeline(context.Background(), "my-pipe", "echo hi", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					_, err = client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "zzznomatch", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(BeEmpty())
				})

				t.Run("prefix matching works for partial status tokens", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					pipeline, err := client.SavePipeline(context.Background(), "my-pipe", "echo hi", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					run1, err := client.SaveRun(context.Background(), pipeline.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusSuccess, "")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchRunsByPipeline(context.Background(), pipeline.ID, "succ", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].ID).To(Equal(run1.ID))
				})

				t.Run("scoped to pipeline - does not return runs from other pipelines", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					client := df.new(t, "ns")
					pipe1, err := client.SavePipeline(context.Background(), "pipe-one", "echo one", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					pipe2, err := client.SavePipeline(context.Background(), "pipe-two", "echo two", "native://", "")
					assert.Expect(err).NotTo(HaveOccurred())
					run1, err := client.SaveRun(context.Background(), pipe1.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run1.ID, storage.RunStatusFailed, "shared-error-token")
					assert.Expect(err).NotTo(HaveOccurred())
					run2, err := client.SaveRun(context.Background(), pipe2.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(context.Background(), run2.ID, storage.RunStatusFailed, "shared-error-token")
					assert.Expect(err).NotTo(HaveOccurred())
					result, err := client.SearchRunsByPipeline(context.Background(), pipe1.ID, "shared", 1, 20)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(result.Items).To(HaveLen(1))
					assert.Expect(result.Items[0].ID).To(Equal(run1.ID))
				})
			})
		})
	}
}
