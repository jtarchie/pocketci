package backwards

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/storage"
)

// JobRunner executes a single job's plan.
type JobRunner struct {
	job      *config.Job
	driver   orchestra.Driver
	storage  storage.Driver
	logger   *slog.Logger
	runID    string
	handlers map[string]StepHandler
}

func newJobRunner(
	job *config.Job,
	driver orchestra.Driver,
	store storage.Driver,
	logger *slog.Logger,
	runID string,
) *JobRunner {
	return &JobRunner{
		job:     job,
		driver:  driver,
		storage: store,
		logger:  logger,
		runID:   runID,
		handlers: map[string]StepHandler{
			"task": &TaskHandler{},
			"try":  &TryHandler{},
			"do":   &DoHandler{},
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
		Ctx:     ctx,
		Driver:  jr.driver,
		Storage: jr.storage,
		Logger:  jr.logger,
		RunID:   jr.runID,
		JobName: jr.job.Name,
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

	if planErr != nil {
		_ = jr.storage.Set(ctx, jobKey, storage.Payload{
			"status": "failure",
		})

		return planErr
	}

	if err := jr.validateAssertions(sc); err != nil {
		_ = jr.storage.Set(ctx, jobKey, storage.Payload{
			"status": "failure",
		})

		return err
	}

	err = jr.storage.Set(ctx, jobKey, storage.Payload{
		"status": "success",
	})
	if err != nil {
		return fmt.Errorf("storage set success: %w", err)
	}

	return nil
}

func (jr *JobRunner) processStep(sc *StepContext, step *config.Step, pathPrefix string) error {
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
		successPrefix := fmt.Sprintf("%s/on_success", pathPrefix)
		if successErr := jr.processStep(sc, step.OnSuccess, successPrefix); successErr != nil {
			stepErr = successErr
		}
	case isAbortError(stepErr) && step.OnAbort != nil:
		sc.Logger.Debug(stepErr.Error())

		abortPrefix := fmt.Sprintf("%s/on_abort", pathPrefix)
		if abortErr := jr.processStep(sc, step.OnAbort, abortPrefix); abortErr != nil {
			sc.Logger.Warn("step.on_abort.failed", "prefix", pathPrefix, "error", abortErr)
		}

		stepErr = nil
	case isErroredError(stepErr) && step.OnError != nil:
		sc.Logger.Debug(stepErr.Error())

		errorPrefix := fmt.Sprintf("%s/on_error", pathPrefix)
		if errorErr := jr.processStep(sc, step.OnError, errorPrefix); errorErr != nil {
			sc.Logger.Warn("step.on_error.failed", "prefix", pathPrefix, "error", errorErr)
		}

		stepErr = nil
	case isFailedError(stepErr) && step.OnFailure != nil:
		failurePrefix := fmt.Sprintf("%s/on_failure", pathPrefix)
		if failureErr := jr.processStep(sc, step.OnFailure, failurePrefix); failureErr != nil {
			sc.Logger.Warn("step.on_failure.failed", "prefix", pathPrefix, "error", failureErr)
		}
	}

	// Ensure hook always runs regardless of step success/failure.
	if step.Ensure != nil {
		ensurePrefix := fmt.Sprintf("%s/ensure", pathPrefix)
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
	case len(step.Try) > 0:
		return "try"
	case len(step.Do) > 0:
		return "do"
	default:
		return ""
	}
}

func (jr *JobRunner) validateAssertions(sc *StepContext) error {
	return validateExecution(fmt.Sprintf("job %q", jr.job.Name), jr.job.Assert.Execution, sc.ExecutedTasks)
}

func formatList(items []string) string {
	return "[" + strings.Join(items, ", ") + "]"
}
