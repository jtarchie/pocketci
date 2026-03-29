package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

const queueTestPipeline = `export const pipeline = async () => { console.log('done'); };`

func TestExecutionQueue(t *testing.T) {
	t.Parallel()

	t.Run("multiple triggers all eventually complete with queue enabled", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		pipeline, err := client.SavePipeline(context.Background(),
			"queue-test", queueTestPipeline, "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:    1,
			MaxQueueSize:   5,
			WebhookTimeout: 100 * time.Millisecond,
		})

		execService := router.ExecutionService()

		// Trigger multiple runs — some may queue, all should complete
		runIDs := make([]string, 0, 3)
		for range 3 {
			run, tErr := execService.TriggerPipeline(context.Background(), pipeline, nil)
			assert.Expect(tErr).NotTo(HaveOccurred())
			runIDs = append(runIDs, run.ID)
		}

		assert.Eventually(func() bool {
			for _, id := range runIDs {
				r, rErr := client.GetRun(context.Background(), id)
				if rErr != nil || !r.Status.IsTerminal() {
					return false
				}
			}

			return true
		}, 10*time.Second, 100*time.Millisecond)

		router.Shutdown()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("MaxQueueSize and QueueLength accessors", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		_, err = client.SavePipeline(context.Background(),
			"accessor-test", queueTestPipeline, "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:    5,
			MaxQueueSize:   42,
			WebhookTimeout: 100 * time.Millisecond,
		})

		execService := router.ExecutionService()
		assert.Expect(execService.MaxQueueSize()).To(Equal(42))
		assert.Expect(execService.MaxInFlight()).To(Equal(5))

		router.Shutdown()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("queue size 0 disables queuing", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		_, err = client.SavePipeline(context.Background(),
			"no-queue-test", queueTestPipeline, "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:    1,
			MaxQueueSize:   0,
			WebhookTimeout: 100 * time.Millisecond,
		})

		assert.Expect(router.ExecutionService().MaxQueueSize()).To(Equal(0))

		// With MaxQueueSize=0, CanAccept degrades to CanExecute
		assert.Expect(router.ExecutionService().CanAccept(context.Background())).To(BeTrue())

		router.Shutdown()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("webhooks accepted when queue has room", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		pipeline, err := client.SavePipeline(context.Background(),
			"webhook-queue-test", queueTestPipeline, "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:    1,
			MaxQueueSize:   10,
			WebhookTimeout: 100 * time.Millisecond,
		})

		// Send multiple webhooks — all should be accepted (not 429)
		for range 3 {
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/"+pipeline.ID, strings.NewReader(`{"test": true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Expect(rec.Code).To(Equal(http.StatusAccepted))
		}

		router.Shutdown()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("restart recovers queued runs", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		pipeline, err := client.SavePipeline(context.Background(),
			"restart-test", queueTestPipeline, "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		// Manually create a queued run as if it survived a restart
		run, err := client.SaveRun(context.Background(), pipeline.ID, storage.TriggerTypeManual, "test", storage.TriggerInput{})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(run.Status).To(Equal(storage.RunStatusQueued))

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:    5,
			MaxQueueSize:   10,
			WebhookTimeout: 100 * time.Millisecond,
		})

		// Simulate server startup behavior
		router.ExecutionService().RecoverOrphanedRuns(context.Background())

		// Queued run should be picked up and complete
		assert.Eventually(func() bool {
			r, rErr := client.GetRun(context.Background(), run.ID)
			return rErr == nil && r.Status.IsTerminal()
		}, 10*time.Second, 100*time.Millisecond)

		router.Shutdown()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("manual trigger accepted via API", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		buildFile, err := os.CreateTemp(t.TempDir(), "")
		assert.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = buildFile.Close() }()

		client, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
		assert.Expect(err).NotTo(HaveOccurred())

		pipeline, err := client.SavePipeline(context.Background(),
			"manual-queue-test", queueTestPipeline, "native", "")
		assert.Expect(err).NotTo(HaveOccurred())

		router := newStrictSecretRouter(t, client, server.RouterOptions{
			MaxInFlight:    1,
			MaxQueueSize:   5,
			WebhookTimeout: 100 * time.Millisecond,
		})

		req := httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipeline.ID+"/trigger", nil)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Expect(rec.Code).To(Equal(http.StatusAccepted))

		router.Shutdown()
		err = client.Close()
		assert.Expect(err).NotTo(HaveOccurred())
	})
}
