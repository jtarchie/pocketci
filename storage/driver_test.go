package storage_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"testing"

	"github.com/jtarchie/pocketci/s3config"
	"github.com/jtarchie/pocketci/storage"
	"github.com/jtarchie/pocketci/storage/s3"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/jtarchie/pocketci/testhelpers"
	. "github.com/onsi/gomega"
)

type driverFactory struct {
	name string
	new  func(t *testing.T, namespace string) storage.Driver
}

func allDrivers() []driverFactory {
	logger := slog.New(slog.DiscardHandler)

	return []driverFactory{
		{
			name: "sqlite",
			new: func(t *testing.T, namespace string) storage.Driver {
				t.Helper()

				f, err := os.CreateTemp(t.TempDir(), "")
				if err != nil {
					t.Fatal(err)
				}

				t.Cleanup(func() { _ = f.Close() })

				client, err := storagesqlite.NewSqlite(storagesqlite.Config{
					Path: f.Name(),
				}, namespace, logger)
				if err != nil {
					t.Fatal(err)
				}

				t.Cleanup(func() { _ = client.Close() })

				return client
			},
		},
		{
			name: "s3",
			new: func(t *testing.T, namespace string) storage.Driver {
				t.Helper()

				if _, err := exec.LookPath("minio"); err != nil {
					t.Skip("minio not installed, skipping S3 storage test")
				}

				server := testhelpers.StartMinIO(t)
				t.Cleanup(server.Stop)

				client, err := s3.NewS3(s3.Config{
					Config: s3config.Config{
						Endpoint:        server.Endpoint(),
						Bucket:          server.Bucket(),
						Region:          "us-east-1",
						AccessKeyID:     "minioadmin",
						SecretAccessKey: "minioadmin",
						ForcePathStyle:  true,
					},
				}, namespace, logger)
				if err != nil {
					t.Fatal(err)
				}

				t.Cleanup(func() { _ = client.Close() })

				return client
			},
		},
	}
}

func TestDrivers(t *testing.T) {
	for _, df := range allDrivers() {
		t.Run(df.name, func(t *testing.T) {
			t.Run("Add Path", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				err := client.Set(context.Background(), "/foo", map[string]string{
					"field":   "123",
					"another": "456",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				results, err := client.GetAll(context.Background(), "/foo", []string{"field"})
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(results).To(HaveLen(1))
				assert.Expect(results[0].Path).To(Equal("/namespace/foo"))
				assert.Expect(results[0].Payload).To(Equal(storage.Payload{
					"field": "123",
				}))

				tree := results.AsTree()
				assert.Expect(tree).To(Equal(&storage.Tree[storage.Payload]{
					Name:     "namespace/foo",
					Children: nil,
					Value: storage.Payload{
						"field": "123",
					},
					FullPath: "/namespace/foo",
				}))
			})

			t.Run("Wildcard returns all fields", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				err := client.Set(context.Background(), "/bar", map[string]any{
					"field":   "123",
					"another": "456",
					"third":   "789",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				results, err := client.GetAll(context.Background(), "/bar", []string{"*"})
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(results).To(HaveLen(1))
				assert.Expect(results[0].Path).To(Equal("/namespace/bar"))
				assert.Expect(results[0].Payload).To(Equal(storage.Payload{
					"field":   "123",
					"another": "456",
					"third":   "789",
				}))
			})

			t.Run("Get not found returns ErrNotFound", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				_, err := client.Get(context.Background(), "/nonexistent")
				assert.Expect(err).To(Equal(storage.ErrNotFound))
			})

			t.Run("SetMerge merges fields into existing payload", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				err := client.Set(context.Background(), "/merge-test", map[string]string{
					"a": "1",
					"b": "2",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(context.Background(), "/merge-test", map[string]string{
					"b": "updated",
					"c": "3",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				payload, err := client.Get(context.Background(), "/merge-test")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(payload["a"]).To(Equal("1"))
				assert.Expect(payload["b"]).To(Equal("updated"))
				assert.Expect(payload["c"]).To(Equal("3"))
			})

			t.Run("UpdateStatusForPrefix updates matching entries", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")

				ctx := context.Background()

				err := client.Set(ctx, "/tasks/1", map[string]string{"status": "running", "name": "task1"})
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.Set(ctx, "/tasks/2", map[string]string{"status": "pending", "name": "task2"})
				assert.Expect(err).NotTo(HaveOccurred())

				err = client.UpdateStatusForPrefix(ctx, "/tasks", []string{"running"}, "cancelled")
				assert.Expect(err).NotTo(HaveOccurred())

				p1, err := client.Get(ctx, "/tasks/1")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(p1["status"]).To(Equal("cancelled"))
				assert.Expect(p1["name"]).To(Equal("task1"))

				p2, err := client.Get(ctx, "/tasks/2")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(p2["status"]).To(Equal("pending"))
			})

			t.Run("Set stores JSON with nested objects and arrays as valid JSONB roundtrip", func(t *testing.T) {
				assert := NewGomegaWithT(t)

				client := df.new(t, "namespace")
				ctx := context.Background()

				payload := map[string]any{
					"status": "success",
					"code":   float64(0),
					"logs": []any{
						map[string]any{"type": "stdout", "content": "hello world\n"},
						map[string]any{"type": "stderr", "content": "warning\n"},
					},
					"nested": map[string]any{
						"key": "value",
					},
				}

				err := client.Set(ctx, "/jsonb-test", payload)
				assert.Expect(err).NotTo(HaveOccurred())

				got, err := client.Get(ctx, "/jsonb-test")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(got["status"]).To(Equal("success"))
				assert.Expect(got["code"]).To(Equal(float64(0)))

				logs, ok := got["logs"].([]any)
				assert.Expect(ok).To(BeTrue())
				assert.Expect(logs).To(HaveLen(2))

				entry0, ok := logs[0].(map[string]any)
				assert.Expect(ok).To(BeTrue())
				assert.Expect(entry0["type"]).To(Equal("stdout"))
				assert.Expect(entry0["content"]).To(Equal("hello world\n"))

				nested, ok := got["nested"].(map[string]any)
				assert.Expect(ok).To(BeTrue())
				assert.Expect(nested["key"]).To(Equal("value"))

				// Verify upsert also preserves JSONB: merge a partial update
				err = client.Set(ctx, "/jsonb-test", map[string]any{
					"status": "failure",
				})
				assert.Expect(err).NotTo(HaveOccurred())

				got2, err := client.Get(ctx, "/jsonb-test")
				assert.Expect(err).NotTo(HaveOccurred())
				assert.Expect(got2["status"]).To(Equal("failure"))
				// Original fields preserved by jsonb_patch
				assert.Expect(got2["code"]).To(Equal(float64(0)))

				logs2, ok := got2["logs"].([]any)
				assert.Expect(ok).To(BeTrue())
				assert.Expect(logs2).To(HaveLen(2))
			})
			t.Run("UpdateRunStatus enforces state machine transitions", func(t *testing.T) {
				t.Run("terminal status blocks non-priority writes", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					ctx := context.Background()
					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(ctx, "sm-test", "content", "native", "")
					assert.Expect(err).NotTo(HaveOccurred())

					// Move run to "failed" (terminal).
					run, err := client.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "stopped")
					assert.Expect(err).NotTo(HaveOccurred())

					// Attempt failed -> success: should be a no-op.
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, "")
					assert.Expect(err).NotTo(HaveOccurred())
					got, err := client.GetRun(ctx, run.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(got.Status).To(Equal(storage.RunStatusFailed))

					// Attempt failed -> running: should be a no-op.
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())
					got, err = client.GetRun(ctx, run.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(got.Status).To(Equal(storage.RunStatusFailed))
				})

				t.Run("success blocks further success or running writes", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					ctx := context.Background()
					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(ctx, "sm-success", "content", "native", "")
					assert.Expect(err).NotTo(HaveOccurred())

					run, err := client.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, "")
					assert.Expect(err).NotTo(HaveOccurred())

					// success -> skipped: no-op
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusSkipped, "")
					assert.Expect(err).NotTo(HaveOccurred())
					got, err := client.GetRun(ctx, run.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(got.Status).To(Equal(storage.RunStatusSuccess))
				})

				t.Run("failed always overwrites any terminal status", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					ctx := context.Background()
					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(ctx, "sm-fail-wins", "content", "native", "")
					assert.Expect(err).NotTo(HaveOccurred())

					run, err := client.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, "")
					assert.Expect(err).NotTo(HaveOccurred())

					// success -> failed: allowed (stop must win)
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "stopped by user")
					assert.Expect(err).NotTo(HaveOccurred())
					got, err := client.GetRun(ctx, run.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(got.Status).To(Equal(storage.RunStatusFailed))
					assert.Expect(got.ErrorMessage).To(Equal("stopped by user"))
				})

				t.Run("queued always overwrites any terminal status", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					ctx := context.Background()
					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(ctx, "sm-resume", "content", "native", "")
					assert.Expect(err).NotTo(HaveOccurred())

					run, err := client.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
					assert.Expect(err).NotTo(HaveOccurred())
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "crash")
					assert.Expect(err).NotTo(HaveOccurred())

					// failed -> queued: allowed (resume must work)
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusQueued, "")
					assert.Expect(err).NotTo(HaveOccurred())
					got, err := client.GetRun(ctx, run.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(got.Status).To(Equal(storage.RunStatusQueued))
				})

				t.Run("non-terminal to terminal transitions work normally", func(t *testing.T) {
					assert := NewGomegaWithT(t)
					ctx := context.Background()
					client := df.new(t, "namespace")

					pipeline, err := client.SavePipeline(ctx, "sm-normal", "content", "native", "")
					assert.Expect(err).NotTo(HaveOccurred())

					run, err := client.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
					assert.Expect(err).NotTo(HaveOccurred())

					// queued -> running: allowed
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusRunning, "")
					assert.Expect(err).NotTo(HaveOccurred())

					// running -> success: allowed
					err = client.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, "")
					assert.Expect(err).NotTo(HaveOccurred())

					got, err := client.GetRun(ctx, run.ID)
					assert.Expect(err).NotTo(HaveOccurred())
					assert.Expect(got.Status).To(Equal(storage.RunStatusSuccess))
				})
			})
		})
	}
}
