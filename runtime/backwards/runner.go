package backwards

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/storage"
)

// ValidateConfig validates the pipeline configuration before execution.
// It checks resource types, job name uniqueness, resource references,
// passed constraint validity, and circular dependency detection.
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

	// Job names must be unique.
	jobNames := make(map[string]bool, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		if jobNames[job.Name] {
			return fmt.Errorf("duplicate job name %q", job.Name)
		}

		jobNames[job.Name] = true
	}

	// Get steps must reference defined resources.
	resourceNames := make(map[string]bool, len(cfg.Resources))
	for _, r := range cfg.Resources {
		resourceNames[r.Name] = true
	}

	for _, job := range cfg.Jobs {
		for _, step := range job.Plan {
			if step.Get != "" {
				ref := step.Get
				if step.GetConfig.Resource != "" {
					ref = step.GetConfig.Resource
				}

				if !resourceNames[ref] {
					return fmt.Errorf("job %q references undefined resource %q", job.Name, ref)
				}
			}
		}
	}

	// Passed constraints must reference existing jobs.
	for _, job := range cfg.Jobs {
		for _, step := range job.Plan {
			if step.Get != "" {
				for _, dep := range step.GetConfig.Passed {
					if !jobNames[dep] {
						return fmt.Errorf("job %q step %q has passed constraint referencing unknown job %q", job.Name, step.Get, dep)
					}
				}
			}
		}
	}

	// Detect circular dependencies in passed constraints using DFS.
	adj := make(map[string][]string, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		adj[job.Name] = extractJobDependencies(job.Plan)
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int, len(cfg.Jobs))

	var visit func(string) error
	visit = func(name string) error {
		color[name] = gray

		for _, dep := range adj[name] {
			switch color[dep] {
			case gray:
				return fmt.Errorf("circular passed constraint: %s -> %s", name, dep)
			case white:
				if err := visit(dep); err != nil {
					return err
				}
			}
		}

		color[name] = black

		return nil
	}

	for _, job := range cfg.Jobs {
		if color[job.Name] == white {
			if err := visit(job.Name); err != nil {
				return err
			}
		}
	}

	return nil
}

// Runner executes a parsed pipeline Config using Go-native execution.
type Runner struct {
	config     *config.Config
	driver     orchestra.Driver
	storage    storage.Driver
	logger     *slog.Logger
	runID      string
	notifier   *jsapi.Notifier
	targetJobs []string
}

// New creates a Runner for the given pipeline config.
func New(
	cfg *config.Config,
	driver orchestra.Driver,
	store storage.Driver,
	logger *slog.Logger,
	runID string,
	notifier *jsapi.Notifier,
	targetJobs []string,
) *Runner {
	return &Runner{
		config:     cfg,
		driver:     driver,
		storage:    store,
		logger:     logger,
		runID:      runID,
		notifier:   notifier,
		targetJobs: targetJobs,
	}
}

// Run executes all jobs respecting passed constraints and validates pipeline-level assertions.
func (r *Runner) Run(ctx context.Context) error {
	jobResults := make(map[string]bool)
	var executedJobs []string

	r.initNotifier()
	r.prewriteJobStates(ctx)

	var runJob func(job *config.Job) error
	runJob = func(job *config.Job) error {
		if _, done := jobResults[job.Name]; done {
			return nil
		}

		jr := newJobRunner(job, r.driver, r.storage, r.logger, r.runID, r.config.Resources, r.config.ResourceTypes, r.config.MaxInFlight, r.notifier)

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

	if len(r.targetJobs) > 0 {
		if err := r.runTargetedJobs(ctx, runJob, jobResults); err != nil {
			return err
		}
	} else {
		for i := range r.config.Jobs {
			job := &r.config.Jobs[i]

			if r.canJobRun(ctx, job, jobResults) {
				if err := runJob(job); err != nil {
					return err
				}
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

// runTargetedJobs executes only the specified target jobs and their downstream
// dependents, marking all other jobs as skipped in storage.
func (r *Runner) runTargetedJobs(
	ctx context.Context,
	runJob func(job *config.Job) error,
	jobResults map[string]bool,
) error {
	jobsByName := make(map[string]*config.Job, len(r.config.Jobs))
	for i := range r.config.Jobs {
		jobsByName[r.config.Jobs[i].Name] = &r.config.Jobs[i]
	}

	for _, jobName := range r.targetJobs {
		job, exists := jobsByName[jobName]
		if !exists {
			return fmt.Errorf("target job %q not found in pipeline", jobName)
		}

		if !r.canJobRun(ctx, job, jobResults) {
			jobKey := fmt.Sprintf("/pipeline/%s/jobs/%s", r.runID, job.Name)

			if err := r.storage.Set(ctx, jobKey, storage.Payload{
				"status": "skipped",
				"reason": "passed constraints not satisfied",
			}); err != nil {
				r.logger.Warn("targeted.skip.failed",
					slog.String("job", job.Name),
					slog.Any("error", err),
				)
			}

			continue
		}

		if err := runJob(job); err != nil {
			return err
		}
	}

	// Mark remaining non-executed jobs as skipped.
	for i := range r.config.Jobs {
		job := &r.config.Jobs[i]

		if _, executed := jobResults[job.Name]; !executed {
			jobKey := fmt.Sprintf("/pipeline/%s/jobs/%s", r.runID, job.Name)

			if err := r.storage.Set(ctx, jobKey, storage.Payload{
				"status": "skipped",
			}); err != nil {
				r.logger.Warn("targeted.mark-skipped.failed",
					slog.String("job", job.Name),
					slog.Any("error", err),
				)
			}
		}
	}

	return nil
}

func (r *Runner) validateAssertions(executedJobs []string) error {
	return validateExecution("pipeline", r.config.Assert.Execution, executedJobs)
}

// initNotifier configures the notification subsystem with pipeline-level configs and context.
func (r *Runner) initNotifier() {
	if r.notifier == nil {
		return
	}

	if len(r.config.Notifications) > 0 {
		r.notifier.SetConfigs(r.config.Notifications)
	}

	pipelineName := "unknown"
	if len(r.config.Jobs) > 0 {
		pipelineName = r.config.Jobs[0].Name
	}

	r.notifier.SetContext(jsapi.NotifyContext{
		PipelineName: pipelineName,
		BuildID:      r.runID,
		Status:       "pending",
		StartTime:    time.Now().UTC().Format(time.RFC3339),
		Environment:  map[string]string{},
		TaskResults:  map[string]any{},
	})
}
