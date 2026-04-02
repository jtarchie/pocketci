package backwards

import (
	"context"
	"fmt"
	"log/slog"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/storage"
)

// ValidateConfig checks that every resource references a defined resource type.
// The "registry-image" type is built-in and always available.
func ValidateConfig(cfg *config.Config) error {
	validTypes := map[string]bool{"registry-image": true}

	for _, rt := range cfg.ResourceTypes {
		validTypes[rt.Name] = true
	}

	for _, resource := range cfg.Resources {
		if !validTypes[resource.Type] {
			return fmt.Errorf("resource %q has undefined resource type %q", resource.Name, resource.Type)
		}
	}

	return nil
}

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

// Run executes all jobs respecting passed constraints and validates pipeline-level assertions.
func (r *Runner) Run(ctx context.Context) error {
	jobResults := make(map[string]bool)
	var executedJobs []string

	r.prewriteJobStates(ctx)

	var runJob func(job *config.Job) error
	runJob = func(job *config.Job) error {
		if _, done := jobResults[job.Name]; done {
			return nil
		}

		jr := newJobRunner(job, r.driver, r.storage, r.logger, r.runID, r.config.Resources, r.config.ResourceTypes, r.config.MaxInFlight)

		err := jr.Run(ctx)
		if err != nil {
			jobResults[job.Name] = false

			return fmt.Errorf("job %q: %w", job.Name, err)
		}

		jobResults[job.Name] = true
		executedJobs = append(executedJobs, job.Name)

		for _, depJob := range r.findDependentJobs(job.Name) {
			if r.canJobRun(ctx, depJob, jobResults) {
				if err := runJob(depJob); err != nil {
					return err
				}
			}
		}

		return nil
	}

	for i := range r.config.Jobs {
		job := &r.config.Jobs[i]

		if r.canJobRun(ctx, job, jobResults) {
			if err := runJob(job); err != nil {
				return err
			}
		}
	}

	if err := r.validateAssertions(executedJobs); err != nil {
		return err
	}

	return nil
}

// canJobRun returns true if all passed constraints for a job are satisfied.
func (r *Runner) canJobRun(ctx context.Context, job *config.Job, jobResults map[string]bool) bool {
	for _, step := range job.Plan {
		if step.Get != "" && len(step.GetConfig.Passed) > 0 {
			for _, dep := range step.GetConfig.Passed {
				if !r.isJobPassedSatisfied(ctx, dep, jobResults) {
					return false
				}
			}
		}
	}

	return true
}

// isJobPassedSatisfied checks if a dependency job has succeeded either
// in the current run or in a prior run via storage.
func (r *Runner) isJobPassedSatisfied(ctx context.Context, depJobName string, jobResults map[string]bool) bool {
	if succeeded, ok := jobResults[depJobName]; ok {
		return succeeded
	}

	status, err := r.storage.GetMostRecentJobStatus(ctx, "", depJobName)
	if err != nil {
		r.logger.Warn("cross-run.check.failed", "job", depJobName, "error", err)

		return false
	}

	return status == "success"
}

// findDependentJobs returns jobs that have a passed constraint referencing jobName.
func (r *Runner) findDependentJobs(jobName string) []*config.Job {
	var result []*config.Job

	for i := range r.config.Jobs {
		job := &r.config.Jobs[i]

		for _, step := range job.Plan {
			if step.Get != "" {
				for _, dep := range step.GetConfig.Passed {
					if dep == jobName {
						result = append(result, job)

						goto nextJob
					}
				}
			}
		}

	nextJob:
	}

	return result
}

// extractJobDependencies returns a deduplicated list of job names referenced
// by passed constraints in the job's plan.
func extractJobDependencies(plan []config.Step) []string {
	var deps []string

	seen := make(map[string]bool)

	for _, step := range plan {
		if step.Get != "" {
			for _, passed := range step.GetConfig.Passed {
				if !seen[passed] {
					seen[passed] = true
					deps = append(deps, passed)
				}
			}
		}
	}

	if deps == nil {
		deps = []string{}
	}

	return deps
}

// prewriteJobStates writes all jobs to storage as pending with dependency
// metadata so the UI can render the full pipeline graph before execution begins.
func (r *Runner) prewriteJobStates(ctx context.Context) {
	for i := range r.config.Jobs {
		job := &r.config.Jobs[i]
		dependsOn := extractJobDependencies(job.Plan)

		payload := storage.Payload{
			"status":    "pending",
			"dependsOn": dependsOn,
		}

		var blockedBy []map[string]string

		for _, step := range job.Plan {
			if step.Get != "" && len(step.GetConfig.Passed) > 0 {
				for _, dep := range step.GetConfig.Passed {
					lastStatus, err := r.storage.GetMostRecentJobStatus(ctx, "", dep)
					if err != nil {
						r.logger.Warn("prewrite.blocked-by.lookup.failed",
							slog.String("job", job.Name),
							slog.String("dependency", dep),
							slog.Any("error", err),
						)

						lastStatus = "never-run"
					}

					if lastStatus != "success" {
						if lastStatus == "" {
							lastStatus = "never-run"
						}

						blockedBy = append(blockedBy, map[string]string{
							"job":        dep,
							"lastStatus": lastStatus,
						})
					}
				}
			}
		}

		if len(blockedBy) > 0 {
			payload["blockedBy"] = blockedBy
		}

		jobKey := fmt.Sprintf("/pipeline/%s/jobs/%s", r.runID, job.Name)

		if err := r.storage.Set(ctx, jobKey, payload); err != nil {
			r.logger.Warn("prewrite.job.failed",
				slog.String("job", job.Name),
				slog.Any("error", err),
			)
		}
	}
}

func (r *Runner) validateAssertions(executedJobs []string) error {
	return validateExecution("pipeline", r.config.Assert.Execution, executedJobs)
}
