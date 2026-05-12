package server_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func webhookDataWithBranch(branch string) *jsapi.WebhookData {
	return &jsapi.WebhookData{
		Provider:  "generic",
		EventType: "test",
		Method:    "POST",
		URL:       "/test",
		Headers:   map[string]string{"X-Branch": branch},
	}
}

const concurrencyTestPipeline = `export const pipeline = async () => { console.log('done'); };`

// concurrencyTestSetup is a small fixture: a router + a pipeline configured
// with the given concurrency mode/template/cancel flag. The pipeline content
// is a near-instant native script — collision behavior is observed via the
// run's status, not its runtime.
type concurrencyTestSetup struct {
	router   *server.Router
	pipeline *storage.Pipeline
	store    storage.Driver
}

func newConcurrencySetup(t *testing.T, mode storage.ConcurrencyMode, groupTemplate string, cancelRunning bool) *concurrencyTestSetup {
	t.Helper()

	dbFile, err := os.CreateTemp(t.TempDir(), "")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	_ = dbFile.Close()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: dbFile.Name()}, "namespace", slog.Default())
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}

	t.Cleanup(func() { _ = store.Close() })

	pipeline, err := store.SavePipeline(context.Background(), "concurrency-test", concurrencyTestPipeline, "native", "")
	if err != nil {
		t.Fatalf("save pipeline: %v", err)
	}

	tmpl := groupTemplate
	cancel := cancelRunning
	pipelineMode := string(mode)

	err = store.UpdatePipeline(context.Background(), pipeline.ID, storage.PipelineUpdate{
		ConcurrencyMode:          &pipelineMode,
		ConcurrencyGroupTemplate: &tmpl,
		ConcurrencyCancelRunning: &cancel,
	})
	if err != nil {
		t.Fatalf("update pipeline concurrency: %v", err)
	}

	pipeline, err = store.GetPipeline(context.Background(), pipeline.ID)
	if err != nil {
		t.Fatalf("re-read pipeline: %v", err)
	}

	router := newStrictSecretRouter(t, store, server.RouterOptions{
		MaxInFlight:    4,
		MaxQueueSize:   10,
		WebhookTimeout: 100 * time.Millisecond,
	})

	t.Cleanup(router.Shutdown)

	return &concurrencyTestSetup{router: router, pipeline: pipeline, store: store}
}

// seedRunningPeer inserts a fake "running" peer for the pipeline so the next
// trigger sees a collision without having to race the runtime.
func seedRunningPeer(t *testing.T, store storage.Driver, pipelineID, group string) *storage.PipelineRun {
	t.Helper()

	run, err := store.SaveRunWithStatus(
		context.Background(),
		pipelineID,
		storage.TriggerTypeManual,
		"seed",
		storage.TriggerInput{},
		group,
		storage.RunStatusRunning,
		"",
	)
	if err != nil {
		t.Fatalf("seed running peer: %v", err)
	}

	return run
}

func seedQueuedPeer(t *testing.T, store storage.Driver, pipelineID, group string) *storage.PipelineRun {
	t.Helper()

	run, err := store.SaveRunWithStatus(
		context.Background(),
		pipelineID,
		storage.TriggerTypeManual,
		"seed",
		storage.TriggerInput{},
		group,
		storage.RunStatusQueued,
		"",
	)
	if err != nil {
		t.Fatalf("seed queued peer: %v", err)
	}

	return run
}

func TestConcurrencyModes(t *testing.T) {
	t.Parallel()

	t.Run("skip-if-running records the new run as skipped", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeSkipIfRunning, "", false)

		peer := seedRunningPeer(t, setup.store, setup.pipeline.ID, "pipeline:"+setup.pipeline.ID)

		newRun, err := setup.router.ExecutionService().TriggerPipeline(context.Background(), setup.pipeline, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(newRun).NotTo(BeNil())
		assert.Expect(newRun.Status).To(Equal(storage.RunStatusSkipped))
		assert.Expect(newRun.ErrorMessage).To(ContainSubstring(peer.ID))

		// Peer is unaffected.
		peerAfter, err := setup.store.GetRun(context.Background(), peer.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(peerAfter.Status).To(Equal(storage.RunStatusRunning))
	})

	t.Run("skip-if-running treats a queued peer as a collision", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeSkipIfRunning, "", false)

		// Saturate slots so the seeded peer cannot dispatch and stays queued.
		seedRunningPeer(t, setup.store, setup.pipeline.ID, "pipeline:"+setup.pipeline.ID)
		queuedPeer := seedQueuedPeer(t, setup.store, setup.pipeline.ID, "pipeline:"+setup.pipeline.ID)

		// A second skip-if-running trigger should still see the queued peer
		// and skip — back-to-back webhooks must not both pass.
		newRun, err := setup.router.ExecutionService().TriggerPipeline(context.Background(), setup.pipeline, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(newRun.Status).To(Equal(storage.RunStatusSkipped))

		queuedAfter, err := setup.store.GetRun(context.Background(), queuedPeer.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(queuedAfter.Status).To(Equal(storage.RunStatusQueued))
	})

	t.Run("serial queues new triggers behind an in-flight peer", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeSerial, "", false)

		seedRunningPeer(t, setup.store, setup.pipeline.ID, "pipeline:"+setup.pipeline.ID)

		newRun, err := setup.router.ExecutionService().TriggerPipeline(context.Background(), setup.pipeline, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(newRun).NotTo(BeNil())
		assert.Expect(newRun.Status).To(Equal(storage.RunStatusQueued))
		assert.Expect(newRun.ConcurrencyGroup).To(Equal("pipeline:" + setup.pipeline.ID))
	})

	t.Run("group queues new triggers in the same group behind a peer", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeGroup, "deploy-{{.Webhook.Branch}}", false)

		seedRunningPeer(t, setup.store, setup.pipeline.ID, "deploy-main")

		newRun, err := setup.router.ExecutionService().TriggerWebhookPipeline(
			context.Background(),
			setup.pipeline,
			webhookDataWithBranch("main"),
			nil,
		)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(newRun.Status).To(Equal(storage.RunStatusQueued))
		assert.Expect(newRun.ConcurrencyGroup).To(Equal("deploy-main"))
	})

	t.Run("group + cancel-in-progress cancels running peers", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeGroup, "deploy-{{.Webhook.Branch}}", true)

		runningPeer := seedRunningPeer(t, setup.store, setup.pipeline.ID, "deploy-main")
		queuedPeer := seedQueuedPeer(t, setup.store, setup.pipeline.ID, "deploy-main")

		newRun, err := setup.router.ExecutionService().TriggerWebhookPipeline(
			context.Background(),
			setup.pipeline,
			webhookDataWithBranch("main"),
			nil,
		)
		assert.Expect(err).NotTo(HaveOccurred())
		// New run was admitted (not skipped); it will dispatch in the
		// background. Its initial status may be queued or running depending
		// on dispatch timing.
		assert.Expect(newRun.Status).To(Or(Equal(storage.RunStatusQueued), Equal(storage.RunStatusRunning)))

		// Running peer should end up failed with a "superseded" reason.
		assert.Eventually(func() bool {
			r, gErr := setup.store.GetRun(context.Background(), runningPeer.ID)
			if gErr != nil {
				return false
			}

			return r.Status == storage.RunStatusFailed
		}, 3*time.Second, 50*time.Millisecond).Should(BeTrue())

		runningAfter, err := setup.store.GetRun(context.Background(), runningPeer.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(runningAfter.ErrorMessage).To(ContainSubstring("superseded"))

		// Queued peer should be marked skipped.
		queuedAfter, err := setup.store.GetRun(context.Background(), queuedPeer.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(queuedAfter.Status).To(Equal(storage.RunStatusSkipped))
		assert.Expect(queuedAfter.ErrorMessage).To(ContainSubstring("superseded"))
	})

	t.Run("group + different group runs in parallel", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeGroup, "deploy-{{.Webhook.Branch}}", false)

		// Running peer in group=deploy-pr-42.
		seedRunningPeer(t, setup.store, setup.pipeline.ID, "deploy-pr-42")

		// New trigger with a different branch -> different group, no collision.
		newRun, err := setup.router.ExecutionService().TriggerWebhookPipeline(
			context.Background(),
			setup.pipeline,
			webhookDataWithBranch("main"),
			nil,
		)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(newRun.ConcurrencyGroup).To(Equal("deploy-main"))
		// Not blocked: queued initially, but with a slot free it should
		// quickly progress to running/terminal.
		assert.Eventually(func() bool {
			r, gErr := setup.store.GetRun(context.Background(), newRun.ID)
			if gErr != nil {
				return false
			}

			return r.Status == storage.RunStatusRunning || r.Status.IsTerminal()
		}, 3*time.Second, 50*time.Millisecond).Should(BeTrue())
	})

	t.Run("template error records the run as failed", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeGroup, "deploy-{{.NotAField}", false)

		newRun, err := setup.router.ExecutionService().TriggerPipeline(context.Background(), setup.pipeline, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(newRun.Status).To(Equal(storage.RunStatusFailed))
		assert.Expect(newRun.ErrorMessage).To(ContainSubstring("concurrency"))
	})

	t.Run("queue processor avoids head-of-line blocking across groups", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeGroup, "deploy-{{.Webhook.Branch}}", false)

		// Pre-seed: running peer holds group=deploy-stuck.
		seedRunningPeer(t, setup.store, setup.pipeline.ID, "deploy-stuck")

		// Queued run A in the same (busy) group — must wait.
		queuedA, err := setup.router.ExecutionService().TriggerWebhookPipeline(
			context.Background(),
			setup.pipeline,
			webhookDataWithBranch("stuck"),
			nil,
		)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(queuedA.Status).To(Equal(storage.RunStatusQueued))

		// Queued run B in a free group — must dispatch around A even though
		// A is older in the queue (FIFO would dispatch A first).
		queuedB, err := setup.router.ExecutionService().TriggerWebhookPipeline(
			context.Background(),
			setup.pipeline,
			webhookDataWithBranch("free"),
			nil,
		)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			r, gErr := setup.store.GetRun(context.Background(), queuedB.ID)
			if gErr != nil {
				return false
			}

			return r.Status == storage.RunStatusSuccess || r.Status == storage.RunStatusRunning
		}, 3*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Run A in the still-busy group should still be queued.
		aAfter, err := setup.store.GetRun(context.Background(), queuedA.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(aAfter.Status).To(Equal(storage.RunStatusQueued))
	})

	t.Run("no concurrency mode preserves legacy behavior", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		setup := newConcurrencySetup(t, storage.ConcurrencyModeNone, "", false)

		seedRunningPeer(t, setup.store, setup.pipeline.ID, "")

		newRun, err := setup.router.ExecutionService().TriggerPipeline(context.Background(), setup.pipeline, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(newRun.ConcurrencyGroup).To(Equal(""))
		assert.Expect(newRun.Status).NotTo(Equal(storage.RunStatusSkipped))
	})
}
