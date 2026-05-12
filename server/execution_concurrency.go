package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"text/template"

	"github.com/jtarchie/pocketci/storage"
)

// concurrencyAction is what a trigger path should do after evaluating
// collision rules.
type concurrencyAction int

const (
	// concurrencyProceed means no peers exist; fast-path dispatch is allowed
	// if a slot is available.
	concurrencyProceed concurrencyAction = iota
	// concurrencyQueueBehind means peers exist; the new run must enter the
	// queue and wait for them to finish. The queue processor is group-aware
	// and will not dispatch this run while peers remain.
	concurrencyQueueBehind
	// concurrencySkip means the trigger should be recorded as a "skipped"
	// run; no execution happens.
	concurrencySkip
	// concurrencyCancelPeers means peers should be cancelled (running) or
	// marked superseded (queued); the new run then proceeds normally.
	concurrencyCancelPeers
)

// concurrencyDecision is the result of evaluating a pipeline's concurrency
// mode against any in-flight peers.
type concurrencyDecision struct {
	Action concurrencyAction
	Group  string // resolved concurrency group key (empty when mode is none)
	Peers  []storage.PipelineRun
	// SkipReason is the human-readable reason persisted in error_message
	// when Action == concurrencySkip.
	SkipReason string
}

// resolveConcurrency evaluates a pipeline's concurrency configuration against
// in-flight peers and returns the action the caller should take. A nil error
// with Action == concurrencySkip means the trigger should be recorded as
// skipped, not failed.
func (s *ExecutionService) resolveConcurrency(ctx context.Context, pipeline *storage.Pipeline, triggerInput storage.TriggerInput) (concurrencyDecision, error) {
	if pipeline.ConcurrencyMode == storage.ConcurrencyModeNone {
		return concurrencyDecision{Action: concurrencyProceed}, nil
	}

	group, err := resolveConcurrencyGroup(pipeline, triggerInput)
	if err != nil {
		return concurrencyDecision{}, fmt.Errorf("resolve concurrency group: %w", err)
	}

	var peers []storage.PipelineRun

	switch pipeline.ConcurrencyMode {
	case storage.ConcurrencyModeSerial, storage.ConcurrencyModeSkipIfRunning:
		// Serial and skip-if-running collide on the whole pipeline regardless
		// of how the group was rendered; querying by pipeline ID is both
		// stricter (catches legacy runs without a group) and cheaper.
		peers, err = s.store.GetActiveRunsByPipeline(ctx, pipeline.ID)
	case storage.ConcurrencyModeGroup:
		peers, err = s.store.GetActiveRunsByGroup(ctx, group)
	default:
		return concurrencyDecision{}, fmt.Errorf("unknown concurrency_mode %q", pipeline.ConcurrencyMode)
	}

	if err != nil {
		return concurrencyDecision{}, fmt.Errorf("lookup active runs: %w", err)
	}

	if len(peers) == 0 {
		return concurrencyDecision{Action: concurrencyProceed, Group: group}, nil
	}

	switch pipeline.ConcurrencyMode {
	case storage.ConcurrencyModeSkipIfRunning:
		return concurrencyDecision{
			Action:     concurrencySkip,
			Group:      group,
			Peers:      peers,
			SkipReason: fmt.Sprintf("skipped: pipeline already running (peer run %s)", peers[0].ID),
		}, nil

	case storage.ConcurrencyModeSerial:
		return concurrencyDecision{Action: concurrencyQueueBehind, Group: group, Peers: peers}, nil

	case storage.ConcurrencyModeGroup:
		if pipeline.ConcurrencyCancelRunning {
			return concurrencyDecision{Action: concurrencyCancelPeers, Group: group, Peers: peers}, nil
		}

		return concurrencyDecision{Action: concurrencyQueueBehind, Group: group, Peers: peers}, nil
	}

	return concurrencyDecision{Action: concurrencyProceed, Group: group}, nil
}

// resolveConcurrencyGroup renders the pipeline's concurrency group key. For
// serial / skip-if-running modes the group collapses to a stable
// pipeline-scoped key. For group mode it evaluates the user template against
// the trigger input.
func resolveConcurrencyGroup(pipeline *storage.Pipeline, triggerInput storage.TriggerInput) (string, error) {
	switch pipeline.ConcurrencyMode {
	case storage.ConcurrencyModeNone:
		return "", nil

	case storage.ConcurrencyModeSerial, storage.ConcurrencyModeSkipIfRunning:
		return "pipeline:" + pipeline.ID, nil

	case storage.ConcurrencyModeGroup:
		if pipeline.ConcurrencyGroupTemplate == "" {
			return "", errors.New("concurrency_mode=group requires concurrency_group_template")
		}

		tmpl, err := template.New("concurrency_group").Option("missingkey=zero").Parse(pipeline.ConcurrencyGroupTemplate)
		if err != nil {
			return "", fmt.Errorf("parse concurrency_group_template: %w", err)
		}

		var buf bytes.Buffer

		err = tmpl.Execute(&buf, concurrencyTemplateData(triggerInput))
		if err != nil {
			return "", fmt.Errorf("execute concurrency_group_template: %w", err)
		}

		group := buf.String()
		if group == "" {
			return "", errors.New("concurrency_group_template rendered to an empty string")
		}

		return group, nil

	default:
		return "", fmt.Errorf("unknown concurrency_mode %q", pipeline.ConcurrencyMode)
	}
}

// concurrencyTemplateData projects the trigger input into a stable surface
// for user templates. Mirrors the fields users are most likely to key off:
// webhook event (provider, branch, ref, event_type) and the arg list.
type concurrencyTemplateContext struct {
	Args    []string
	Jobs    []string
	Webhook concurrencyWebhookContext
}

type concurrencyWebhookContext struct {
	Provider  string
	EventType string
	Method    string
	URL       string
	// Branch and Ref are convenience projections of the X-Branch / X-Ref
	// headers (a documented convention; templates can also read Headers
	// directly for provider-specific keys).
	Branch  string
	Ref     string
	Headers map[string]string
	Query   map[string]string
}

func concurrencyTemplateData(triggerInput storage.TriggerInput) concurrencyTemplateContext {
	ctx := concurrencyTemplateContext{
		Args: triggerInput.Args,
		Jobs: triggerInput.Jobs,
	}

	if triggerInput.Webhook != nil {
		ctx.Webhook = concurrencyWebhookContext{
			Provider:  triggerInput.Webhook.Provider,
			EventType: triggerInput.Webhook.EventType,
			Method:    triggerInput.Webhook.Method,
			URL:       triggerInput.Webhook.URL,
			Branch:    triggerInput.Webhook.Headers["X-Branch"],
			Ref:       triggerInput.Webhook.Headers["X-Ref"],
			Headers:   triggerInput.Webhook.Headers,
			Query:     triggerInput.Webhook.Query,
		}
	}

	return ctx
}

// supersedePeers cancels in-flight peer runs and marks queued peers as
// skipped because a newer run is taking over their concurrency group.
// Used by group-mode + cancel-in-progress.
func (s *ExecutionService) supersedePeers(ctx context.Context, peers []storage.PipelineRun, supersedingRunID string, logger *slog.Logger) {
	for _, peer := range peers {
		reason := "superseded by run " + supersedingRunID

		switch peer.Status {
		case storage.RunStatusRunning:
			// StopRun cancels the in-process context and writes "failed" with
			// "Run stopped by user"; overwrite the error_message so the audit
			// trail reflects the real cause.
			stopErr := s.StopRun(peer.ID) //nolint:contextcheck // deliberate: in-process cancel of a separate goroutine
			if stopErr != nil && !errors.Is(stopErr, ErrRunNotInFlight) {
				logger.Warn("concurrency.supersede.stop_failed",
					slog.String("peer_run_id", peer.ID), slog.String("error", stopErr.Error()))
			}

			_ = s.store.UpdateRunStatus(ctx, peer.ID, storage.RunStatusFailed, reason)

			logger.Info("concurrency.supersede",
				slog.String("peer_run_id", peer.ID),
				slog.String("peer_status", string(peer.Status)))

		case storage.RunStatusQueued:
			updErr := s.store.UpdateRunStatus(ctx, peer.ID, storage.RunStatusSkipped, reason)
			if updErr != nil {
				logger.Warn("concurrency.supersede.skip_failed",
					slog.String("peer_run_id", peer.ID), slog.String("error", updErr.Error()))

				continue
			}

			logger.Info("concurrency.supersede",
				slog.String("peer_run_id", peer.ID),
				slog.String("peer_status", string(peer.Status)))

		case storage.RunStatusSuccess, storage.RunStatusFailed, storage.RunStatusSkipped:
			// Terminal — nothing to do.
		}
	}
}

// admissionResult is the outcome of evaluating a new trigger against the
// pipeline's concurrency rules.
type admissionResult struct {
	// Run is the persisted run record. Always non-nil on success (including
	// terminal skipped/failed runs).
	Run *storage.PipelineRun
	// Terminal is true when the run was admitted directly as a skipped or
	// failed terminal run; callers must not dispatch it.
	Terminal bool
	// QueueOnly forbids the fast-path: even if a slot is free the run must
	// wait in queued status for the group-aware processor to dispatch it.
	QueueOnly bool
}

// admitRun applies a pipeline's concurrency rules and persists the new run.
// Template/config errors are recorded as failed terminal runs (Terminal=true);
// the caller treats that as a successful admission.
func (s *ExecutionService) admitRun(
	ctx context.Context,
	pipeline *storage.Pipeline,
	triggerType storage.TriggerType,
	triggeredBy string,
	triggerInput storage.TriggerInput,
) (admissionResult, error) {
	decision, err := s.resolveConcurrency(ctx, pipeline, triggerInput)
	if err != nil {
		s.logger.Warn("concurrency.config.invalid",
			slog.String("pipeline_id", pipeline.ID),
			slog.String("error", err.Error()))

		failed, saveErr := s.store.SaveRunWithStatus(ctx, pipeline.ID, triggerType, triggeredBy, triggerInput, "", storage.RunStatusFailed, err.Error())
		if saveErr != nil {
			return admissionResult{}, fmt.Errorf("save failed run: %w", saveErr)
		}

		return admissionResult{Run: failed, Terminal: true}, nil
	}

	if decision.Action == concurrencySkip {
		s.metrics().CounterAdd("pocketci_runs_skipped_total", 1, map[string]string{"reason": "skip-if-running"})

		skipped, saveErr := s.store.SaveRunWithStatus(ctx, pipeline.ID, triggerType, triggeredBy, triggerInput, decision.Group, storage.RunStatusSkipped, decision.SkipReason)
		if saveErr != nil {
			return admissionResult{}, fmt.Errorf("save skipped run: %w", saveErr)
		}

		s.logger.Info("concurrency.skip",
			slog.String("run_id", skipped.ID),
			slog.String("pipeline_id", pipeline.ID),
			slog.String("group", decision.Group))

		return admissionResult{Run: skipped, Terminal: true}, nil
	}

	run, err := s.store.SaveRun(ctx, pipeline.ID, triggerType, triggeredBy, triggerInput, decision.Group)
	if err != nil {
		return admissionResult{}, fmt.Errorf("save run: %w", err)
	}

	if decision.Action == concurrencyCancelPeers {
		s.metrics().CounterAdd("pocketci_runs_skipped_total", 1, map[string]string{"reason": "superseded"})
		s.supersedePeers(ctx, decision.Peers, run.ID, s.logger)
	}

	return admissionResult{
		Run:       run,
		QueueOnly: decision.Action == concurrencyQueueBehind,
	}, nil
}

// dispatchOrQueue takes a non-terminal admitted run and either dispatches it
// immediately or leaves it queued for the processor. queueOnly forbids the
// fast-path because the run's concurrency group is busy; the group-aware
// queue processor will pick it up when peers clear.
// Returns true if the run was dispatched on the fast path.
func (s *ExecutionService) dispatchOrQueue(pipeline *storage.Pipeline, run *storage.PipelineRun, opts execOptions, queueOnly bool) bool {
	if !queueOnly && s.CanExecute() {
		s.dispatchRun(pipeline, run, opts)

		return true
	}

	if queueOnly {
		s.logger.Info("queue.enqueued.group_busy", "run_id", run.ID, "pipeline_id", pipeline.ID, "group", run.ConcurrencyGroup)
	} else {
		s.logger.Info("queue.enqueued", "run_id", run.ID, "pipeline_id", pipeline.ID)
	}

	s.cond.Broadcast()

	return false
}

// busyConcurrencyGroups returns the set of group keys currently held by
// running (not queued) peers. The queue processor uses this to skip
// dispatching queued runs whose group is busy, without head-of-line
// blocking other groups.
func (s *ExecutionService) busyConcurrencyGroups(ctx context.Context) (map[string]struct{}, error) {
	running, err := s.store.GetRunsByStatus(ctx, storage.RunStatusRunning, 0)
	if err != nil {
		return nil, fmt.Errorf("list running runs: %w", err)
	}

	busy := make(map[string]struct{}, len(running))

	for _, r := range running {
		if r.ConcurrencyGroup == "" {
			continue
		}

		busy[r.ConcurrencyGroup] = struct{}{}
	}

	return busy, nil
}
