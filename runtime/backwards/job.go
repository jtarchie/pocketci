package backwards

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/storage"
)

// JobRunner executes a single job's plan.
type JobRunner struct {
	job                 *config.Job
	driver              orchestra.Driver
	storage             storage.Driver
	logger              *slog.Logger
	runID               string
	handlers            map[string]StepHandler
	resources           config.Resources
	resourceTypes       config.ResourceTypes
	pipelineMaxInFlight int
	notifier            *jsapi.Notifier
}

func newJobRunner(
	job *config.Job,
	driver orchestra.Driver,
	store storage.Driver,
	logger *slog.Logger,
	runID string,
	resources config.Resources,
	resourceTypes config.ResourceTypes,
	pipelineMaxInFlight int,
	notifier *jsapi.Notifier,
) *JobRunner {
	return &JobRunner{
		job:                 job,
		driver:              driver,
		storage:             store,
		logger:              logger,
		runID:               runID,
		resources:           resources,
		resourceTypes:       resourceTypes,
		pipelineMaxInFlight: pipelineMaxInFlight,
		notifier:            notifier,
		handlers: map[string]StepHandler{
			"task":        &TaskHandler{},
			"get":         &GetHandler{},
			"put":         &PutHandler{},
			"try":         &TryHandler{},
			"do":          &DoHandler{},
			"in_parallel": &InParallelHandler{},
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

	sc := &StepContext{
		Ctx:           ctx,
		Driver:        jr.driver,
		Storage:       jr.storage,
		Logger:        jr.logger,
		RunID:         jr.runID,
		JobName:       jr.job.Name,
		MaxInFlight:   resolveEffectiveMaxInFlight(jr.job.MaxInFlight, jr.pipelineMaxInFlight),
		CacheVolumes:  make(map[string]string),
		KnownVolumes:  make(map[string]string),
		Resources:     jr.resources,
		ResourceTypes: jr.resourceTypes,
		JobParams:     extractJobParams(jr.job),
		Notifier:      jr.notifier,
	}
	sc.ProcessStep = func(step *config.Step, pathPrefix string) error {
		return jr.processStep(sc, step, pathPrefix)
	}

	var planErr error

	for i, step := range jr.job.Plan {
		padded := zeroPadWithLength(i, len(jr.job.Plan))

		if err := jr.processStep(sc, &step, padded); err != nil {
			planErr = err

			break
		}
	}

	planErr = jr.runJobHooks(sc, planErr)

	// Always validate job-level assertions, even after plan errors.
	if assertErr := jr.validateAssertions(sc); assertErr != nil {
		_ = jr.storage.Set(ctx, jobKey, storage.Payload{
			"status": "failure",
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
				"status": "failure",
			})

			return planErr
		}
	}

	err = jr.storage.Set(ctx, jobKey, storage.Payload{
		"status": "success",
	})
	if err != nil {
		return fmt.Errorf("storage set success: %w", err)
	}

	return nil
}

// runJobHooks runs job-level hooks (on_failure, on_abort, on_error, on_success, ensure)
// and returns the remaining planErr (nil if a hook handled it).
func (jr *JobRunner) runJobHooks(sc *StepContext, planErr error) error {
	jobFailed := planErr != nil || sc.HadFailure

	if jobFailed {
		switch {
		case isAbortError(planErr) && jr.job.OnAbort != nil:
			sc.Logger.Debug(planErr.Error())

			if abortErr := jr.processStep(sc, jr.job.OnAbort, "job/on_abort"); abortErr != nil {
				sc.Logger.Warn("job.on_abort.failed", "job", jr.job.Name, "error", abortErr)
			}

			planErr = nil
		case isErroredError(planErr) && jr.job.OnError != nil:
			sc.Logger.Debug(planErr.Error())

			if errorErr := jr.processStep(sc, jr.job.OnError, "job/on_error"); errorErr != nil {
				sc.Logger.Warn("job.on_error.failed", "job", jr.job.Name, "error", errorErr)
			}

			planErr = nil
		case (isFailedError(planErr) || planErr == nil) && jr.job.OnFailure != nil:
			if failureErr := jr.processStep(sc, jr.job.OnFailure, "job/on_failure"); failureErr != nil {
				sc.Logger.Warn("job.on_failure.failed", "job", jr.job.Name, "error", failureErr)
			}

			planErr = nil
		}

		if jr.job.Ensure != nil {
			if ensureErr := jr.processStep(sc, jr.job.Ensure, "job/ensure"); ensureErr != nil {
				sc.Logger.Warn("job.ensure.failed", "job", jr.job.Name, "error", ensureErr)
			}
		}
	} else if jr.job.OnSuccess != nil {
		if successErr := jr.processStep(sc, jr.job.OnSuccess, "job/on_success"); successErr != nil {
			sc.Logger.Warn("job.on_success.failed", "job", jr.job.Name, "error", successErr)
		}
	}

	return planErr
}

func (jr *JobRunner) processStep(sc *StepContext, step *config.Step, pathPrefix string) error {
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

	stepErr := handler.Execute(sc, step, pathPrefix)

	// on_success / on_abort / on_error / on_failure hooks run before ensure.
	switch {
	case stepErr == nil && step.OnSuccess != nil:
		successPrefix := pathPrefix + "/on_success"
		if successErr := jr.processStep(sc, step.OnSuccess, successPrefix); successErr != nil {
			stepErr = successErr
		}
	case isAbortError(stepErr) && step.OnAbort != nil:
		sc.Logger.Debug(stepErr.Error())
		sc.HadFailure = true

		abortPrefix := pathPrefix + "/on_abort"
		if abortErr := jr.processStep(sc, step.OnAbort, abortPrefix); abortErr != nil {
			sc.Logger.Warn("step.on_abort.failed", "prefix", pathPrefix, "error", abortErr)
		}

		stepErr = nil
	case isErroredError(stepErr) && step.OnError != nil:
		sc.Logger.Debug(stepErr.Error())
		sc.HadFailure = true

		errorPrefix := pathPrefix + "/on_error"
		if errorErr := jr.processStep(sc, step.OnError, errorPrefix); errorErr != nil {
			sc.Logger.Warn("step.on_error.failed", "prefix", pathPrefix, "error", errorErr)
		}

		stepErr = nil
	case isFailedError(stepErr) && step.OnFailure != nil:
		sc.HadFailure = true

		failurePrefix := pathPrefix + "/on_failure"
		if failureErr := jr.processStep(sc, step.OnFailure, failurePrefix); failureErr != nil {
			sc.Logger.Warn("step.on_failure.failed", "prefix", pathPrefix, "error", failureErr)
		}

		stepErr = nil
	}

	// Ensure hook always runs regardless of step success/failure.
	if step.Ensure != nil {
		ensurePrefix := pathPrefix + "/ensure"
		if ensureErr := jr.processStep(sc, step.Ensure, ensurePrefix); ensureErr != nil {
			sc.Logger.Warn("step.ensure.failed", "prefix", pathPrefix, "error", ensureErr)
		}
	}

	return stepErr
}

func identifyStepType(step *config.Step) string {
	switch {
	case step.Task != "":
		return "task"
	case step.Get != "":
		return "get"
	case step.Put != "":
		return "put"
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

func extractJobParams(job *config.Job) map[string]string {
	if job.Triggers == nil || job.Triggers.Webhook == nil {
		return nil
	}

	return job.Triggers.Webhook.Params
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
