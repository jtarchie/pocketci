package backwards

import (
	"context"
	"fmt"
	"log/slog"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/storage"
)

// Runner executes a parsed pipeline Config using Go-native execution.
type Runner struct {
	config  *config.Config
	driver  orchestra.Driver
	storage storage.Driver
	logger  *slog.Logger
	runID   string
}

// New creates a Runner for the given pipeline config.
func New(
	cfg *config.Config,
	driver orchestra.Driver,
	store storage.Driver,
	logger *slog.Logger,
	runID string,
) *Runner {
	return &Runner{
		config:  cfg,
		driver:  driver,
		storage: store,
		logger:  logger,
		runID:   runID,
	}
}

// Run executes all jobs and validates pipeline-level assertions.
func (r *Runner) Run(ctx context.Context) error {
	var executedJobs []string

	for i := range r.config.Jobs {
		job := &r.config.Jobs[i]

		jr := newJobRunner(job, r.driver, r.storage, r.logger, r.runID)

		err := jr.Run(ctx)
		if err != nil {
			return fmt.Errorf("job %q: %w", job.Name, err)
		}

		executedJobs = append(executedJobs, job.Name)
	}

	if err := r.validateAssertions(executedJobs); err != nil {
		return err
	}

	return nil
}

func (r *Runner) validateAssertions(executedJobs []string) error {
	return validateExecution("pipeline", r.config.Assert.Execution, executedJobs)
}

