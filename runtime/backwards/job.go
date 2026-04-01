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

	return handler.Execute(sc, step, pathPrefix)
}

func identifyStepType(step *config.Step) string {
	switch {
	case step.Task != "":
		return "task"
	case len(step.Try) > 0:
		return "try"
	default:
		return ""
	}
}

func (jr *JobRunner) validateAssertions(sc *StepContext) error {
	if jr.job.Assert.Execution == nil {
		return nil
	}

	expected := jr.job.Assert.Execution
	got := sc.ExecutedTasks

	if len(expected) != len(got) {
		return &AssertionError{
			Message: fmt.Sprintf("job %q execution: expected %s, got %s",
				jr.job.Name, formatList(expected), formatList(got)),
		}
	}

	for i := range expected {
		if expected[i] != got[i] {
			return &AssertionError{
				Message: fmt.Sprintf("job %q execution[%d]: expected %q, got %q",
					jr.job.Name, i, expected[i], got[i]),
			}
		}
	}

	return nil
}

func formatList(items []string) string {
	return "[" + strings.Join(items, ", ") + "]"
}
