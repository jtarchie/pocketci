package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
)

// scanPassedDownstreams runs after a successful pipeline_run and triggers
// any downstream jobs whose triggers.passed is now satisfied — i.e. every
// named upstream has a successful run since the downstream's last run of
// any status.
//
// The scanner is stateless and idempotent: it only reads pipeline_runs and
// fires through the existing admit + dispatch path. Crashes between
// upstream commit and trigger are recovered by the boot-time sweep
// (see recoverPassedTriggers).
func (s *ExecutionService) scanPassedDownstreams(ctx context.Context, pipeline *storage.Pipeline, completedRun *storage.PipelineRun, logger *slog.Logger) {
	if pipeline == nil || completedRun == nil {
		return
	}

	if pipeline.Paused {
		// Paused pipelines never accrue passed-triggered runs. Mirrors the
		// scheduler-callback guard.
		return
	}

	// Serialize concurrent completions: without this, two upstreams that
	// finish at the same instant can both pass the coalescing check and
	// both queue a duplicate downstream run. The scanner is brief —
	// reads + a single admitRun — so contention is negligible.
	s.scannerMu.Lock()
	defer s.scannerMu.Unlock()

	cfg, err := backwards.ParseConfig(pipeline.Content)
	if err != nil {
		// Not a YAML pipeline (or unparseable); nothing to scan.
		return
	}

	completedJobs := completedJobNames(completedRun, cfg)
	if len(completedJobs) == 0 {
		return
	}

	s.metrics().CounterAdd("pocketci_passed_scans_total", 1, nil)

	downstreams := buildPassedDependents(cfg)
	if len(downstreams) == 0 {
		return
	}

	// Each completed job may have many downstreams; dedup so the same
	// downstream isn't evaluated twice per scan.
	candidates := make(map[string]*backwards.Job)

	for _, jobName := range completedJobs {
		for _, downstream := range downstreams[jobName] {
			candidates[downstream.Name] = downstream
		}
	}

	for _, downstream := range candidates {
		s.evaluatePassedDownstream(ctx, pipeline, downstream, completedRun, logger)
	}
}

// completedJobNames returns the set of job names the completed run executed.
// For runs with TargetJobs set, that's the explicit list. For untargeted
// runs, it's every job in the pipeline plan (the legacy "fire all jobs"
// behavior — the runner may have filtered some out, but their statuses
// aren't success so freshness checks downstream won't find them).
func completedJobNames(run *storage.PipelineRun, cfg *backwards.Config) []string {
	if len(run.TriggerInput.Jobs) > 0 {
		return run.TriggerInput.Jobs
	}

	names := make([]string, 0, len(cfg.Jobs))
	for _, j := range cfg.Jobs {
		names = append(names, j.Name)
	}

	return names
}

// buildPassedDependents indexes jobs by upstream name: a key U maps to the
// list of jobs whose triggers.passed contains U. O(1) lookup during the
// scan.
func buildPassedDependents(cfg *backwards.Config) map[string][]*backwards.Job {
	dependents := make(map[string][]*backwards.Job)

	for i := range cfg.Jobs {
		job := &cfg.Jobs[i]

		if job.Triggers == nil || len(job.Triggers.Passed) == 0 {
			continue
		}

		for _, upstream := range job.Triggers.Passed {
			dependents[upstream] = append(dependents[upstream], job)
		}
	}

	return dependents
}

// evaluatePassedDownstream checks if `downstream`'s triggers.passed is
// fully satisfied and, if so, admits a new run targeting it.
func (s *ExecutionService) evaluatePassedDownstream(ctx context.Context, pipeline *storage.Pipeline, downstream *backwards.Job, completedRun *storage.PipelineRun, logger *slog.Logger) {
	downstreamLogger := logger.With("downstream_job", downstream.Name)

	// Coalescing: don't queue a second run when one's already pending.
	active, err := s.store.GetActiveRunsByPipeline(ctx, pipeline.ID)
	if err != nil {
		downstreamLogger.Error("concurrency.passed.scan.active_runs_failed", "error", err)
		return
	}

	for _, r := range active {
		for _, j := range r.TriggerInput.Jobs {
			if j == downstream.Name {
				s.metrics().CounterAdd("pocketci_passed_coalesced_total", 1,
					map[string]string{"pipeline": pipeline.ID, "job": downstream.Name})

				downstreamLogger.Info("concurrency.passed.coalesce",
					slog.String("existing_run_id", r.ID),
					slog.String("existing_status", string(r.Status)),
				)

				return
			}
		}
	}

	// Freshness reference: B's most recent run of any status.
	lastRun, err := s.store.GetMostRecentJobRun(ctx, pipeline.ID, downstream.Name)
	if err != nil {
		downstreamLogger.Error("concurrency.passed.scan.last_run_failed", "error", err)
		return
	}

	var sinceTime = time.Time{}
	if lastRun != nil {
		sinceTime = lastRun.CreatedAt
	}

	upstreamRunIDs := make([]string, 0, len(downstream.Triggers.Passed))

	for _, upstream := range downstream.Triggers.Passed {
		fresh, fErr := s.store.GetSuccessfulJobRunSince(ctx, pipeline.ID, upstream, sinceTime)
		if fErr != nil {
			downstreamLogger.Error("concurrency.passed.scan.upstream_check_failed",
				slog.String("upstream", upstream), slog.Any("error", fErr))
			return
		}

		if fresh == nil {
			downstreamLogger.Info("concurrency.passed.waiting",
				slog.String("waiting_on", upstream),
				slog.Time("since", sinceTime),
			)

			return
		}

		upstreamRunIDs = append(upstreamRunIDs, fresh.ID)
	}

	// All upstreams satisfied — fire.
	s.firePassedDownstream(ctx, pipeline, downstream, completedRun, upstreamRunIDs, downstreamLogger)
}

// firePassedDownstream admits a new pipeline_run targeting the downstream
// job with TriggerType=passed and UpstreamRunIDs recording the lineage.
func (s *ExecutionService) firePassedDownstream(ctx context.Context, pipeline *storage.Pipeline, downstream *backwards.Job, completedRun *storage.PipelineRun, upstreamRunIDs []string, logger *slog.Logger) {
	if !s.CanAccept(ctx) {
		logger.Warn("concurrency.passed.queue_full")
		return
	}

	triggerInput := storage.TriggerInput{
		Jobs:           []string{downstream.Name},
		UpstreamRunIDs: upstreamRunIDs,
	}

	triggeredBy := "passed:" + completedRun.ID

	admission, err := s.admitRun(ctx, pipeline, storage.TriggerTypePassed, triggeredBy, triggerInput)
	if err != nil {
		logger.Error("concurrency.passed.admit_failed", "error", err)
		return
	}

	if admission.Terminal {
		// Skipped or failed at admission (e.g., template error, skip-if-running);
		// the run record still exists for audit. Nothing more to do.
		return
	}

	opts := execOptions{jobs: []string{downstream.Name}}
	s.dispatchOrQueue(pipeline, admission.Run, opts, admission.QueueOnly) //nolint:contextcheck // dispatchRun creates its own background context

	s.metrics().CounterAdd("pocketci_passed_triggers_total", 1,
		map[string]string{"pipeline": pipeline.ID, "job": downstream.Name})

	logger.Info("concurrency.passed.trigger",
		slog.String("new_run_id", admission.Run.ID),
		slog.Any("upstream_run_ids", upstreamRunIDs),
	)
}

// recoverPassedTriggers runs a one-time sweep at server startup: for every
// pipeline with at least one triggers.passed job, evaluate whether each
// downstream would fire right now. Idempotent (it relies on the same
// coalescing check as scanPassedDownstreams), so safe to run after every
// restart.
func (s *ExecutionService) recoverPassedTriggers(ctx context.Context, logger *slog.Logger) {
	// Iterate all pipelines. SearchPipelines with an empty query returns
	// every pipeline; perPage 1000 keeps the read bounded for typical
	// deployments without paging.
	page, err := s.store.SearchPipelines(ctx, "", 1, 1000)
	if err != nil {
		logger.Error("concurrency.passed.recover.list_failed", "error", err)
		return
	}

	for i := range page.Items {
		pipeline := &page.Items[i]

		if pipeline.Paused {
			continue
		}

		cfg, parseErr := backwards.ParseConfig(pipeline.Content)
		if parseErr != nil {
			continue
		}

		hasPassed := false
		for _, j := range cfg.Jobs {
			if j.Triggers != nil && len(j.Triggers.Passed) > 0 {
				hasPassed = true
				break
			}
		}

		if !hasPassed {
			continue
		}

		for ji := range cfg.Jobs {
			job := &cfg.Jobs[ji]

			if job.Triggers == nil || len(job.Triggers.Passed) == 0 {
				continue
			}

			s.evaluatePassedDownstream(ctx, pipeline, job, nil, logger.With("recover", true, "pipeline_id", pipeline.ID))
		}
	}

	logger.Info("concurrency.passed.recover.done", slog.Int("pipelines_scanned", len(page.Items)))
}

