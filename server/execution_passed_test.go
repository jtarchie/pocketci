package server_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/backwards"
	runtimebackwards "github.com/jtarchie/pocketci/runtime/backwards"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

// validatePipelineYAMLForTest mirrors the validation path the upsert
// handler runs — parse + ValidateConfig. Returns the first error.
func validatePipelineYAMLForTest(yamlText string) error {
	cfg, err := backwards.ParseConfig(yamlText)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	err = runtimebackwards.ValidateConfig(cfg, nil)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return nil
}

// passedTestPipelineYAML defines a four-job DAG:
//
//	A ─┐
//	   ├─> B ─> C
//	D ─┘
//
// A and D have leaf triggers (cron/webhook). B fires on triggers.passed:[A,D].
// C fires on triggers.passed:[B].
const passedTestPipelineYAML = `
jobs:
  - name: a
    triggers:
      schedule:
        cron: "0 1 * * *"
    plan:
      - task: echo
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: busybox }
          run: { path: echo, args: ["a ok"] }

  - name: d
    triggers:
      webhook: {}
    plan:
      - task: echo
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: busybox }
          run: { path: echo, args: ["d ok"] }

  - name: b
    triggers:
      passed: [a, d]
    plan:
      - task: echo
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: busybox }
          run: { path: echo, args: ["b ok"] }

  - name: c
    triggers:
      passed: [b]
    plan:
      - task: echo
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: busybox }
          run: { path: echo, args: ["c ok"] }
`

type passedFixture struct {
	router   *server.Router
	pipeline *storage.Pipeline
	store    storage.Driver
}

func newPassedFixture(t *testing.T) *passedFixture {
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

	pipeline, err := store.SavePipeline(context.Background(), "passed-test", passedTestPipelineYAML, "native", "yaml")
	if err != nil {
		t.Fatalf("save pipeline: %v", err)
	}

	router := newStrictSecretRouter(t, store, server.RouterOptions{
		MaxInFlight:    4,
		MaxQueueSize:   10,
		WebhookTimeout: 100 * time.Millisecond,
	})

	t.Cleanup(router.Shutdown)

	return &passedFixture{router: router, pipeline: pipeline, store: store}
}

// seedSuccessfulRun inserts a "success" pipeline_run targeting the given
// job. Used to fake upstream completion timestamps without actually
// running the pipeline.
func seedSuccessfulRun(t *testing.T, store storage.Driver, pipelineID, jobName string) *storage.PipelineRun {
	t.Helper()

	run, err := store.SaveRunWithStatus(
		context.Background(),
		pipelineID,
		storage.TriggerTypeManual,
		"seed",
		storage.TriggerInput{Jobs: []string{jobName}},
		"",
		storage.RunStatusSuccess,
		"",
	)
	if err != nil {
		t.Fatalf("seed successful run for %s: %v", jobName, err)
	}

	// Tiny pause to keep ordering strict between sequential seeds.
	time.Sleep(10 * time.Millisecond)

	return run
}

// findRunsForJob returns all runs (any status) targeting jobName.
func findRunsForJob(t *testing.T, store storage.Driver, jobName string) []storage.PipelineRun {
	t.Helper()

	var matches []storage.PipelineRun

	for _, status := range []storage.RunStatus{
		storage.RunStatusQueued,
		storage.RunStatusRunning,
		storage.RunStatusSuccess,
		storage.RunStatusFailed,
		storage.RunStatusSkipped,
	} {
		runs, err := store.GetRunsByStatus(context.Background(), status, 0)
		if err != nil {
			t.Fatalf("get runs by status %q: %v", status, err)
		}

		for _, r := range runs {
			for _, j := range r.TriggerInput.Jobs {
				if j == jobName {
					matches = append(matches, r)
				}
			}
		}
	}

	return matches
}

func TestTriggersPassed(t *testing.T) {
	t.Parallel()

	t.Run("does not fire when only one upstream succeeded", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		fx := newPassedFixture(t)

		// Seed: a succeeds but d has not.
		seedSuccessfulRun(t, fx.store, fx.pipeline.ID, "a")

		// Manually invoke the scanner against a's completion.
		// We can use the public TriggerScheduledPipeline since admit goes
		// through the same path; but for unit-test isolation we just call
		// scanPassedDownstreams indirectly via TriggerScheduledPipeline.
		execService := fx.router.ExecutionService()

		// Trigger a real `a` run via the scheduler path so finalizeExecRun
		// calls the scanner naturally.
		run, err := execService.TriggerScheduledPipeline(context.Background(), fx.pipeline, "test-cron", "a")
		assert.Expect(err).NotTo(HaveOccurred())

		// Wait for a to finish and the scanner to evaluate.
		assert.Eventually(func() bool {
			r, gErr := fx.store.GetRun(context.Background(), run.ID)
			return gErr == nil && r.Status.IsTerminal()
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		// B should NOT have been triggered: d has no fresh success.
		time.Sleep(200 * time.Millisecond) // small grace for scanner

		bRuns := findRunsForJob(t, fx.store, "b")
		assert.Expect(bRuns).To(BeEmpty(), "B should not fire while D has no fresh success")
	})

	t.Run("fires once when both upstreams have a fresh success", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		fx := newPassedFixture(t)
		execService := fx.router.ExecutionService()

		// Run a via schedule path → succeeds → scanner sees d not fresh → no B.
		aRun, err := execService.TriggerScheduledPipeline(context.Background(), fx.pipeline, "test-cron", "a")
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			r, gErr := fx.store.GetRun(context.Background(), aRun.ID)
			return gErr == nil && r.Status.IsTerminal()
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Trigger d via schedule path with a target job to keep the test
		// independent of webhook plumbing. The runner allows d to run because
		// TargetJobs overrides the trigger-type filter.
		dRun, err := execService.TriggerScheduledPipeline(context.Background(), fx.pipeline, "d-trigger", "d")
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			r, gErr := fx.store.GetRun(context.Background(), dRun.ID)
			return gErr == nil && r.Status.IsTerminal()
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		// After D succeeds, scanner should fire B.
		assert.Eventually(func() bool {
			return len(findRunsForJob(t, fx.store, "b")) >= 1
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		bRuns := findRunsForJob(t, fx.store, "b")
		assert.Expect(bRuns).To(HaveLen(1))
		assert.Expect(bRuns[0].TriggerType).To(Equal(storage.TriggerTypePassed))
		assert.Expect(bRuns[0].TriggerInput.UpstreamRunIDs).To(HaveLen(2))
	})

	t.Run("cascades B → C", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		fx := newPassedFixture(t)
		execService := fx.router.ExecutionService()

		// Seed: A and D both succeeded before this run.
		seedSuccessfulRun(t, fx.store, fx.pipeline.ID, "a")
		seedSuccessfulRun(t, fx.store, fx.pipeline.ID, "d")

		// Trigger an A run; its success should cascade B → C.
		aRun, err := execService.TriggerScheduledPipeline(context.Background(), fx.pipeline, "kickoff", "a")
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			r, gErr := fx.store.GetRun(context.Background(), aRun.ID)
			return gErr == nil && r.Status == storage.RunStatusSuccess
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		// B fires (A fresh from this run, D fresh from seed).
		assert.Eventually(func() bool {
			return len(findRunsForJob(t, fx.store, "b")) >= 1
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		// B's eventually completes, triggering C.
		assert.Eventually(func() bool {
			return len(findRunsForJob(t, fx.store, "c")) >= 1
		}, 10*time.Second, 50*time.Millisecond).Should(BeTrue())
	})

	t.Run("coalescing: scanner skips if a run for the downstream is already queued", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		fx := newPassedFixture(t)

		// Seed a B run in queued status that hasn't started yet.
		_, err := fx.store.SaveRunWithStatus(
			context.Background(),
			fx.pipeline.ID,
			storage.TriggerTypePassed,
			"seed",
			storage.TriggerInput{Jobs: []string{"b"}},
			"",
			storage.RunStatusQueued,
			"",
		)
		assert.Expect(err).NotTo(HaveOccurred())

		// Seed A and D fresh successes; the scanner would otherwise fire B.
		seedSuccessfulRun(t, fx.store, fx.pipeline.ID, "a")
		seedSuccessfulRun(t, fx.store, fx.pipeline.ID, "d")

		// Trigger one more A run to cause the scanner to evaluate B.
		execService := fx.router.ExecutionService()

		aRun, err := execService.TriggerScheduledPipeline(context.Background(), fx.pipeline, "scan-trigger", "a")
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Eventually(func() bool {
			r, gErr := fx.store.GetRun(context.Background(), aRun.ID)
			return gErr == nil && r.Status.IsTerminal()
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Grace for the scanner.
		time.Sleep(200 * time.Millisecond)

		// Exactly one B run should exist (the seeded queued one). Coalescing
		// prevented the scanner from queuing a second.
		bRuns := findRunsForJob(t, fx.store, "b")
		assert.Expect(bRuns).To(HaveLen(1))
	})
}

func TestTriggersPassedValidation(t *testing.T) {
	t.Parallel()

	t.Run("upsert rejects self-reference", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		yaml := `
jobs:
  - name: a
    triggers:
      webhook: {}
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
  - name: b
    triggers:
      passed: [b]
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
`
		err := validatePipelineYAMLForTest(yaml)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("references itself"))
	})

	t.Run("upsert rejects unknown upstream", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		yaml := `
jobs:
  - name: a
    triggers:
      webhook: {}
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
  - name: b
    triggers:
      passed: [does-not-exist]
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
`
		err := validatePipelineYAMLForTest(yaml)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("unknown job"))
	})

	t.Run("upsert rejects cycle through triggers.passed", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		yaml := `
jobs:
  - name: a
    triggers:
      webhook: {}
      passed: [b]
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
  - name: b
    triggers:
      passed: [a]
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
`
		err := validatePipelineYAMLForTest(yaml)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("circular"))
	})

	t.Run("upsert rejects dead-chain pipelines with no leaf trigger", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		yaml := `
jobs:
  - name: a
    triggers:
      passed: [b]
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
  - name: b
    triggers:
      passed: [a]
    plan:
      - task: noop
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
`
		err := validatePipelineYAMLForTest(yaml)
		assert.Expect(err).To(HaveOccurred())
		// Either the cycle check or the leaf check rejects this; both messages
		// are acceptable since the user clearly violated both rules.
		assert.Expect(err.Error()).To(Or(
			ContainSubstring("leaf trigger"),
			ContainSubstring("circular"),
		))
	})

	t.Run("upsert rejects passed: on a task step", func(t *testing.T) {
		assert := NewGomegaWithT(t)
		yaml := `
jobs:
  - name: a
    triggers:
      webhook: {}
    plan:
      - task: noop
        passed: [some-job]
        config:
          platform: linux
          image_resource: { type: registry-image, source: { repository: busybox } }
          run: { path: true }
`
		err := validatePipelineYAMLForTest(yaml)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("`passed:` is only supported on `get` steps"))
	})
}
