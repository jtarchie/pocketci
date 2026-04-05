package backwards

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
	"github.com/jtarchie/pocketci/webhooks/filter"
)

const gatePollInterval = 2 * time.Second

// JobRunner executes a single job's plan.
type JobRunner struct {
	job                 *config.Job
	driver              orchestra.Driver
	storage             storage.Driver
	logger              *slog.Logger
	runID               string
	pipelineID          string
	handlers            map[string]StepHandler
	resources           config.Resources
	resourceTypes       config.ResourceTypes
	pipelineMaxInFlight int
	notifier            *jsapi.Notifier
	webhookData         *jsapi.WebhookData
	dedupTTL            time.Duration
	secretsManager      secrets.Manager
	agentBaseURLs       map[string]string
}

func newJobRunner(
	job *config.Job,
	driver orchestra.Driver,
	store storage.Driver,
	logger *slog.Logger,
	runID string,
	pipelineID string,
	resources config.Resources,
	resourceTypes config.ResourceTypes,
	pipelineMaxInFlight int,
	notifier *jsapi.Notifier,
	webhookData *jsapi.WebhookData,
	dedupTTL time.Duration,
	secretsManager secrets.Manager,
	agentBaseURLs map[string]string,
) *JobRunner {
	return &JobRunner{
		job:                 job,
		driver:              driver,
		storage:             store,
		logger:              logger,
		runID:               runID,
		pipelineID:          pipelineID,
		resources:           resources,
		resourceTypes:       resourceTypes,
		pipelineMaxInFlight: pipelineMaxInFlight,
		notifier:            notifier,
		webhookData:         webhookData,
		dedupTTL:            dedupTTL,
		secretsManager:      secretsManager,
		agentBaseURLs:       agentBaseURLs,
		handlers: map[string]StepHandler{
			"task":        &TaskHandler{},
			"get":         &GetHandler{},
			"put":         &PutHandler{},
			"try":         &TryHandler{},
			"do":          &DoHandler{},
			"in_parallel": &InParallelHandler{},
			"notify":      &NotifyHandler{},
			"agent":       &AgentHandler{},
		},
	}
}

func (jr *JobRunner) Run(ctx context.Context) error {
	jobKey := fmt.Sprintf("/pipeline/%s/jobs/%s", jr.runID, jr.job.Name)

	err := jr.storage.Set(ctx, jobKey, storage.Payload{
		"status": "pending",
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	// Evaluate webhook filter before executing the job.
	if skip := jr.evaluateWebhookFilter(ctx, jobKey); skip {
		return nil
	}

	// Evaluate webhook dedup before executing the job.
	if skip := jr.evaluateWebhookDedup(ctx, jobKey); skip {
		return nil
	}

	pr := pipelinerunner.NewPipelineRunner(ctx, jr.driver, jr.storage, jr.logger, jr.job.Name, jr.runID)

	sc := &StepContext{
		Ctx:                ctx,
		Driver:             jr.driver,
		Storage:            jr.storage,
		Logger:             jr.logger,
		RunID:              jr.runID,
		JobName:            jr.job.Name,
		MaxInFlight:        resolveEffectiveMaxInFlight(jr.job.MaxInFlight, jr.pipelineMaxInFlight),
		CacheVolumes:       make(map[string]string),
		CacheVolumeObjects: make(map[string]orchestra.Volume),
		KnownVolumes:       make(map[string]string),
		Resources:          jr.resources,
		ResourceTypes:      jr.resourceTypes,
		JobParams:          extractJobParams(jr.job, jr.webhookData),
		Notifier:           jr.notifier,
		PipelineRunner:     pr,
		SecretsManager:     jr.secretsManager,
		PipelineID:         jr.pipelineID,
		AgentBaseURLs:      jr.agentBaseURLs,
	}
	sc.ProcessStep = func(step *config.Step, pathPrefix string) error { //nolint:contextcheck // context flows via sc.Ctx; StepHandler interface cannot accept a context parameter
		return jr.processStep(sc, step, pathPrefix)
	}

	// Clean up cache volumes at job end so S3-backed caches are persisted.
	// This must happen after all tasks (including hooks) finish — not per-task —
	// so volumes remain live for tasks sharing the same cache within a job.
	defer jr.cleanupCacheVolumes(sc)

	if jr.job.Gate != nil {
		err := jr.runGate(ctx)
		if err != nil {
			_ = jr.storage.Set(ctx, jobKey, storage.Payload{"status": "failure"})

			return fmt.Errorf("gate: %w", err)
		}
	}

	var planErr error

	for i, step := range jr.job.Plan {
		padded := zeroPadWithLength(i, len(jr.job.Plan))

		err := jr.processStep(sc, &step, padded) //nolint:contextcheck // context is in sc.Ctx
		if err != nil {
			planErr = err
			jr.markRemainingStepsSkipped(ctx, sc, i+1)

			break
		}
	}

	planErr = jr.runJobHooks(sc, planErr) //nolint:contextcheck // context is in sc.Ctx

	// Always validate job-level assertions, even after plan errors.
	assertErr := jr.validateAssertions(sc)
	if assertErr != nil {
		_ = jr.storage.Set(ctx, jobKey, storage.Payload{
			"status":       "failure",
			"errorMessage": assertErr.Error(),
		})

		return assertErr
	}

	// Step-level assertion errors always propagate — they are test correctness
	// checks that should not be masked by passing job assertions.
	// Task execution failures (non-zero exit, errored, aborted) are cleared when
	// job assertions pass, since the execution order was expected.
	if planErr != nil {
		isStepAssertionErr := errors.Is(planErr, ErrAssertionFailed)

		if isStepAssertionErr || jr.job.Assert == nil {
			_ = jr.storage.Set(ctx, jobKey, storage.Payload{
				"status":       "failure",
				"errorMessage": planErr.Error(),
			})

			return planErr
		}
	}

	// When a task failed but the job-level assertion cleared planErr, still
	// record the error message so the UI can show why a task failed.
	successPayload := storage.Payload{"status": "success"}
	if planErr != nil {
		successPayload["errorMessage"] = planErr.Error()
	}

	err = jr.storage.Set(ctx, jobKey, successPayload)
	if err != nil {
		return fmt.Errorf("storage set success: %w", err)
	}

	return nil
}

func (jr *JobRunner) cleanupCacheVolumes(sc *StepContext) {
	for path, vol := range sc.CacheVolumeObjects {
		err := vol.Cleanup(sc.Ctx)
		if err != nil {
			jr.logger.Warn("cache.volume.cleanup.failed", "path", path, "err", err)
		}
	}
}

func (jr *JobRunner) markRemainingStepsSkipped(ctx context.Context, sc *StepContext, from int) {
	for j := from; j < len(jr.job.Plan); j++ {
		skippedStep := jr.job.Plan[j]
		identifier := stepStorageIdentifier(&skippedStep)

		if identifier == "" {
			continue
		}

		skippedPadded := zeroPadWithLength(j, len(jr.job.Plan))
		skippedKey := fmt.Sprintf("%s/%s/%s", sc.BaseStorageKey(), skippedPadded, identifier)

		_ = jr.storage.Set(ctx, skippedKey, storage.Payload{
			"status": "skipped",
		})
	}
}

func (jr *JobRunner) dispatchJobFailureHook(sc *StepContext, effectiveKind FailureKind, planErr error) error {
	switch {
	case effectiveKind == FailureKindAborted && jr.job.OnAbort != nil:
		if planErr != nil {
			sc.Logger.Debug(planErr.Error())
		}

		abortErr := jr.processStep(sc, jr.job.OnAbort, "job/on_abort")
		if abortErr != nil {
			sc.Logger.Warn("job.on_abort.failed", "job", jr.job.Name, "error", abortErr)
		}

		return nil
	case effectiveKind == FailureKindErrored && jr.job.OnError != nil:
		if planErr != nil {
			sc.Logger.Debug(planErr.Error())
		}

		errorErr := jr.processStep(sc, jr.job.OnError, "job/on_error")
		if errorErr != nil {
			sc.Logger.Warn("job.on_error.failed", "job", jr.job.Name, "error", errorErr)
		}

		return nil
	case (effectiveKind == FailureKindFailed || effectiveKind == FailureKindNone) && jr.job.OnFailure != nil:
		failureErr := jr.processStep(sc, jr.job.OnFailure, "job/on_failure")
		if failureErr != nil {
			sc.Logger.Warn("job.on_failure.failed", "job", jr.job.Name, "error", failureErr)
		}

		return nil
	}

	return planErr
}

// runJobHooks runs job-level hooks (on_failure, on_abort, on_error, on_success, ensure)
// and returns the remaining planErr (nil if a hook handled it).
func (jr *JobRunner) runJobHooks(sc *StepContext, planErr error) error {
	// Assertion errors are test correctness failures, not task failures.
	// They must propagate without being consumed by job-level hooks.
	if errors.Is(planErr, ErrAssertionFailed) {
		if jr.job.Ensure != nil {
			ensureErr := jr.processStep(sc, jr.job.Ensure, "job/ensure")
			if ensureErr != nil {
				jr.logger.Warn("job.ensure.failed", "job", jr.job.Name, "error", ensureErr)
			}
		}

		return planErr
	}

	jobFailed := planErr != nil || sc.FailureCount > 0

	// Determine the effective failure kind for job-level hook dispatch.
	// When planErr is nil but a step-level failure was handled, use LastFailureKind.
	effectiveKind := FailureKindNone
	if planErr != nil {
		switch {
		case isAbortError(planErr):
			effectiveKind = FailureKindAborted
		case isErroredError(planErr):
			effectiveKind = FailureKindErrored
		case isFailedError(planErr):
			effectiveKind = FailureKindFailed
		}
	} else if sc.FailureCount > 0 {
		effectiveKind = sc.LastFailureKind
	}

	if jobFailed {
		planErr = jr.dispatchJobFailureHook(sc, effectiveKind, planErr)
	} else if jr.job.OnSuccess != nil {
		successErr := jr.processStep(sc, jr.job.OnSuccess, "job/on_success")
		if successErr != nil {
			sc.Logger.Warn("job.on_success.failed", "job", jr.job.Name, "error", successErr)
		}
	}

	// Ensure always runs regardless of job success or failure.
	if jr.job.Ensure != nil {
		ensureErr := jr.processStep(sc, jr.job.Ensure, "job/ensure")
		if ensureErr != nil {
			sc.Logger.Warn("job.ensure.failed", "job", jr.job.Name, "error", ensureErr)
		}
	}

	return planErr
}

func (jr *JobRunner) dispatchInnerFailureHook(sc *StepContext, step *config.Step, pathPrefix string) {
	switch {
	case sc.LastFailureKind == FailureKindAborted && step.OnAbort != nil:
		sc.Logger.Debug("try.abort.outer")

		abortPrefix := pathPrefix + "/on_abort"
		err := jr.processStep(sc, step.OnAbort, abortPrefix)
		if err != nil {
			sc.Logger.Warn("step.on_abort.failed", "prefix", pathPrefix, "error", err)
		}
	case sc.LastFailureKind == FailureKindErrored && step.OnError != nil:
		sc.Logger.Debug("try.error.outer")

		errorPrefix := pathPrefix + "/on_error"
		err := jr.processStep(sc, step.OnError, errorPrefix)
		if err != nil {
			sc.Logger.Warn("step.on_error.failed", "prefix", pathPrefix, "error", err)
		}
	case step.OnFailure != nil:
		failurePrefix := pathPrefix + "/on_failure"
		err := jr.processStep(sc, step.OnFailure, failurePrefix)
		if err != nil {
			sc.Logger.Warn("step.on_failure.failed", "prefix", pathPrefix, "error", err)
		}
	}
}

// dispatchOuterErrorHook runs the step-level error hook for stepErr.
// Returns true if a hook consumed the error (stepErr should become nil).
func (jr *JobRunner) dispatchOuterErrorHook(sc *StepContext, step *config.Step, pathPrefix string, stepErr error) bool {
	switch {
	case isAbortError(stepErr) && step.OnAbort != nil:
		sc.Logger.Debug(stepErr.Error())
		sc.FailureCount++
		sc.LastFailureKind = FailureKindAborted

		abortPrefix := pathPrefix + "/on_abort"
		err := jr.processStep(sc, step.OnAbort, abortPrefix)
		if err != nil {
			sc.Logger.Warn("step.on_abort.failed", "prefix", pathPrefix, "error", err)
		}

		return true
	case isErroredError(stepErr) && step.OnError != nil:
		sc.Logger.Debug(stepErr.Error())
		sc.FailureCount++
		sc.LastFailureKind = FailureKindErrored

		errorPrefix := pathPrefix + "/on_error"
		err := jr.processStep(sc, step.OnError, errorPrefix)
		if err != nil {
			sc.Logger.Warn("step.on_error.failed", "prefix", pathPrefix, "error", err)
		}

		return true
	case isFailedError(stepErr) && step.OnFailure != nil:
		sc.FailureCount++
		sc.LastFailureKind = FailureKindFailed

		failurePrefix := pathPrefix + "/on_failure"
		err := jr.processStep(sc, step.OnFailure, failurePrefix)
		if err != nil {
			sc.Logger.Warn("step.on_failure.failed", "prefix", pathPrefix, "error", err)
		}

		return true
	}

	return false
}

func (jr *JobRunner) dispatchStepHook(sc *StepContext, step *config.Step, pathPrefix string, stepErr error, failureBefore int) error {
	failureInside := sc.FailureCount > failureBefore && stepErr == nil

	if stepErr == nil && !failureInside && step.OnSuccess != nil {
		successPrefix := pathPrefix + "/on_success"
		successErr := jr.processStep(sc, step.OnSuccess, successPrefix)
		if successErr != nil {
			return successErr
		}

		return nil
	}

	if failureInside {
		jr.dispatchInnerFailureHook(sc, step, pathPrefix)

		return nil
	}

	if jr.dispatchOuterErrorHook(sc, step, pathPrefix, stepErr) {
		return nil
	}

	return stepErr
}

func (jr *JobRunner) processStep(sc *StepContext, step *config.Step, pathPrefix string) error {
	// Inject webhook trigger params into task env before dispatch.
	injectJobParams(sc.JobParams, step)

	// Handle across expansion before normal step dispatch.
	if len(step.Across) > 0 {
		return executeAcross(sc, step, pathPrefix, func(s *config.Step, prefix string) error {
			return jr.processStep(sc, s, prefix)
		})
	}

	// Handle attempts retry before normal step dispatch.
	// Attempts strips hooks from the inner step and manages them after all retries.
	if step.Attempts > 1 {
		return jr.executeWithAttempts(sc, step, pathPrefix)
	}

	stepType := identifyStepType(step)
	if stepType == "" {
		return fmt.Errorf("unknown step type in job %q at prefix %q", jr.job.Name, pathPrefix)
	}

	handler, ok := jr.handlers[stepType]
	if !ok {
		return fmt.Errorf("no handler registered for step type %q", stepType)
	}

	failureBefore := sc.FailureCount
	stepErr := handler.Execute(sc, step, pathPrefix)

	stepErr = jr.dispatchStepHook(sc, step, pathPrefix, stepErr, failureBefore)

	// Ensure hook always runs regardless of step success/failure.
	if step.Ensure != nil {
		ensurePrefix := pathPrefix + "/ensure"
		ensureErr := jr.processStep(sc, step.Ensure, ensurePrefix)
		if ensureErr != nil {
			sc.Logger.Warn("step.ensure.failed", "prefix", pathPrefix, "error", ensureErr)
		}
	}

	return stepErr
}

// injectJobParams merges webhook trigger params into the step's TaskConfig.Env
// before handler dispatch. Only task steps with an inline config are affected;
// file/URI-based configs are re-injected after loading inside runTask.
func injectJobParams(jobParams map[string]string, step *config.Step) {
	if len(jobParams) == 0 {
		return
	}

	if step.Task == "" || step.TaskConfig == nil {
		return
	}

	step.TaskConfig.Env = mergeJobParams(jobParams, step.TaskConfig.Env)
}

func identifyStepType(step *config.Step) string {
	switch {
	case step.Task != "":
		return "task"
	case step.Agent != "":
		return "agent"
	case step.Get != "":
		return "get"
	case step.Put != "":
		return "put"
	case step.Notify != nil:
		return "notify"
	case len(step.Try) > 0:
		return "try"
	case len(step.Do) > 0:
		return "do"
	case len(step.InParallel.Steps) > 0:
		return "in_parallel"
	default:
		return ""
	}
}

func (jr *JobRunner) validateAssertions(sc *StepContext) error {
	if jr.job.Assert == nil {
		return nil
	}

	return validateExecution(fmt.Sprintf("job %q", jr.job.Name), jr.job.Assert.Execution, sc.ExecutedTasks)
}

func stepStorageIdentifier(step *config.Step) string {
	switch {
	case step.Task != "":
		return "tasks/" + step.Task
	case step.Agent != "":
		return "agent/" + step.Agent
	case step.Get != "":
		return "get/" + step.Get
	case step.Put != "":
		return "put/" + step.Put
	case step.Notify != nil:
		return "notify/" + notifyIdentifier(step)
	case len(step.Do) > 0:
		return "do"
	case len(step.Try) > 0:
		return "try"
	case len(step.InParallel.Steps) > 0:
		return "in_parallel"
	case len(step.Across) > 0:
		return "across"
	default:
		return ""
	}
}

// defaultDedupTTL is the default time-to-live for webhook dedup entries.
const defaultDedupTTL = 7 * 24 * time.Hour

// evaluateWebhookFilter checks the webhook filter expression. Returns true if
// the job should be skipped (filter didn't match or errored).
func (jr *JobRunner) evaluateWebhookFilter(ctx context.Context, jobKey string) bool {
	webhookFilter := ""
	if jr.job.Triggers != nil && jr.job.Triggers.Webhook != nil {
		webhookFilter = jr.job.Triggers.Webhook.Filter
	}

	if webhookFilter == "" {
		webhookFilter = jr.job.WebhookTrigger // deprecated field
	}

	if webhookFilter == "" || jr.webhookData == nil {
		return false // no filter or manual trigger — always run
	}

	env := filter.BuildWebhookEnv(jr.webhookData)

	pass, err := filter.Evaluate(webhookFilter, env)
	if err != nil {
		jr.logger.Error("webhook.filter.failed", slog.String("error", err.Error()), slog.String("expression", webhookFilter))

		pass = false
	}

	if !pass {
		_ = jr.storage.Set(ctx, jobKey, storage.Payload{"status": "skipped"})

		return true
	}

	return false
}

// evaluateWebhookDedup checks the dedup key expression. Returns true if
// the job should be skipped (duplicate webhook).
func (jr *JobRunner) evaluateWebhookDedup(ctx context.Context, jobKey string) bool {
	if jr.job.Triggers == nil || jr.job.Triggers.Webhook == nil || jr.job.Triggers.Webhook.DedupKey == "" {
		return false
	}

	if jr.webhookData == nil {
		return false // manual triggers are never duplicates
	}

	env := filter.BuildWebhookEnv(jr.webhookData)

	keyHash, err := filter.DedupKeyHash(jr.job.Triggers.Webhook.DedupKey, env)
	if err != nil {
		jr.logger.Error("webhook.dedup.eval.failed", slog.String("error", err.Error()))

		return false // on error, don't skip
	}

	if keyHash == nil {
		return false // empty key, no dedup
	}

	ttl := jr.dedupTTL
	if ttl == 0 {
		ttl = defaultDedupTTL
	}

	cutoff := time.Now().UTC().Add(-ttl)
	if _, pruneErr := jr.storage.PruneWebhookDedup(ctx, cutoff); pruneErr != nil {
		jr.logger.Warn("webhook.dedup.prune.failed", slog.String("error", pruneErr.Error()))
	}

	isDup, err := jr.storage.RecordWebhookDedup(ctx, jr.pipelineID, keyHash)
	if err != nil {
		jr.logger.Error("webhook.dedup.record.failed", slog.String("error", err.Error()))

		return false
	}

	if isDup {
		_ = jr.storage.Set(ctx, jobKey, storage.Payload{"status": "skipped"})

		return true
	}

	return false
}

func extractJobParams(job *config.Job, webhookData *jsapi.WebhookData) map[string]string {
	if job.Triggers == nil || job.Triggers.Webhook == nil || len(job.Triggers.Webhook.Params) == 0 {
		return nil
	}

	if webhookData == nil {
		return make(map[string]string) // manual trigger: empty params
	}

	env := filter.BuildWebhookEnv(webhookData)
	result := make(map[string]string, len(job.Triggers.Webhook.Params))

	for key, expression := range job.Triggers.Webhook.Params {
		val, err := filter.EvaluateString(expression, env)
		if err != nil {
			slog.Error("webhook.params.eval.failed", slog.String("key", key), slog.String("error", err.Error()))

			continue
		}

		result[key] = val
	}

	return result
}

func (jr *JobRunner) runGate(ctx context.Context) error {
	gateID := uuid.New().String()
	gate := &storage.Gate{
		ID:         gateID,
		RunID:      jr.runID,
		PipelineID: jr.pipelineID,
		Name:       jr.job.Name,
		Status:     storage.GateStatusPending,
		Message:    jr.job.Gate.Message,
	}

	createdAt := time.Now()

	err := jr.storage.SaveGate(ctx, gate)
	if err != nil {
		return fmt.Errorf("save gate: %w", err)
	}

	return jr.pollGate(ctx, gateID, createdAt)
}

func (jr *JobRunner) pollGate(ctx context.Context, gateID string, createdAt time.Time) error {
	var deadline time.Time

	if jr.job.Gate.Timeout != "" {
		dur, err := time.ParseDuration(jr.job.Gate.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout %q: %w", jr.job.Gate.Timeout, err)
		}

		deadline = createdAt.Add(dur)
	}

	for {
		if !deadline.IsZero() && time.Now().After(deadline) {
			err := jr.storage.ResolveGate(ctx, gateID, storage.GateStatusTimedOut, "timeout")
			if err != nil {
				jr.logger.Error("gate.resolve.timeout.failed",
					slog.String("gate_id", gateID),
					slog.String("job", jr.job.Name),
					slog.Any("error", err),
				)
			}

			return fmt.Errorf("gate %q: timed out", jr.job.Name)
		}

		gate, err := jr.storage.GetGate(ctx, gateID)
		if err != nil {
			return fmt.Errorf("gate %q: poll failed: %w", jr.job.Name, err)
		}

		switch gate.Status {
		case storage.GateStatusApproved:
			return nil
		case storage.GateStatusRejected:
			return fmt.Errorf("gate %q: rejected by %s", jr.job.Name, gate.ApprovedBy)
		case storage.GateStatusTimedOut:
			return fmt.Errorf("gate %q: timed out", jr.job.Name)
		case storage.GateStatusPending:
			// continue polling
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gatePollInterval):
		}
	}
}

// resolveEffectiveMaxInFlight returns the effective max_in_flight for a job.
// Priority: job-level > pipeline-level > 0 (not set).
func resolveEffectiveMaxInFlight(jobLevel, pipelineLevel int) int {
	if jobLevel > 0 {
		return jobLevel
	}

	return pipelineLevel
}

func formatList(items []string) string {
	return "[" + strings.Join(items, ", ") + "]"
}
