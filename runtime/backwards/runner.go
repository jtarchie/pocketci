package backwards

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/resources"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

// emptyRegistry is used when no registry is provided to avoid nil checks everywhere.
var emptyRegistry = resources.NewRegistry(nil)

func validateResourceTypes(cfg *config.Config, registry *resources.Registry) error {
	validTypes := map[string]bool{"registry-image": true}

	for _, rt := range cfg.ResourceTypes {
		validTypes[rt.Name] = true
	}

	for _, r := range cfg.Resources {
		if !validTypes[r.Type] && !registry.IsNative(r.Type) {
			return fmt.Errorf("resource %q has undefined resource type %q", r.Name, r.Type)
		}
	}

	return nil
}

func validateJobNames(cfg *config.Config) (map[string]bool, error) {
	jobNames := make(map[string]bool, len(cfg.Jobs))

	for _, job := range cfg.Jobs {
		if jobNames[job.Name] {
			return nil, fmt.Errorf("duplicate job name %q", job.Name)
		}

		jobNames[job.Name] = true
	}

	return jobNames, nil
}

func validateResourceRefs(cfg *config.Config) error {
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

	return nil
}

func validatePassedConstraints(cfg *config.Config, jobNames map[string]bool) error {
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

	return nil
}

func validateNoCycles(cfg *config.Config) error {
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
				err := visit(dep)
				if err != nil {
					return err
				}
			}
		}

		color[name] = black

		return nil
	}

	for _, job := range cfg.Jobs {
		if color[job.Name] == white {
			err := visit(job.Name)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// ValidateConfig validates the pipeline configuration before execution.
// It checks resource types, job name uniqueness, resource references,
// passed constraint validity, and circular dependency detection.
// Pass nil for registry to use an empty registry (no native resources).
func ValidateConfig(cfg *config.Config, registry *resources.Registry) error {
	if registry == nil {
		registry = emptyRegistry
	}

	err := validateResourceTypes(cfg, registry)
	if err != nil {
		return err
	}

	jobNames, err := validateJobNames(cfg)
	if err != nil {
		return err
	}

	err = validateResourceRefs(cfg)
	if err != nil {
		return err
	}

	err = validatePassedConstraints(cfg, jobNames)
	if err != nil {
		return err
	}

	return validateNoCycles(cfg)
}

// RunnerOptions holds optional configuration for a Runner.
type RunnerOptions struct {
	Notifier         *jsapi.Notifier
	TargetJobs       []string
	WebhookData      *jsapi.WebhookData
	DedupTTL         time.Duration
	SecretsManager   secrets.Manager
	ResourceRegistry *resources.Registry       // native resource implementations; nil uses an empty registry
	AgentBaseURLs    map[string]string         // overrides agent provider base URLs; used in tests to avoid global state
	OutputCallback   func(stream, data string) // called for each chunk of task stdout/stderr
}

// Runner executes a parsed pipeline Config using Go-native execution.
type Runner struct {
	config           *config.Config
	driver           orchestra.Driver
	storage          storage.Driver
	logger           *slog.Logger
	runID            string
	pipelineID       string
	notifier         *jsapi.Notifier
	targetJobs       []string
	webhookData      *jsapi.WebhookData
	dedupTTL         time.Duration
	secretsManager   secrets.Manager
	resourceRegistry *resources.Registry
	agentBaseURLs    map[string]string
	outputCallback   func(stream, data string)
	dependents       map[string][]*config.Job // reverse index: jobName → jobs that depend on it
}

// New creates a Runner for the given pipeline config.
func New(
	cfg *config.Config,
	driver orchestra.Driver,
	store storage.Driver,
	logger *slog.Logger,
	runID string,
	pipelineID string,
	opts RunnerOptions,
) *Runner {
	registry := opts.ResourceRegistry
	if registry == nil {
		registry = emptyRegistry
	}

	return &Runner{
		config:           cfg,
		driver:           driver,
		storage:          store,
		logger:           logger,
		runID:            runID,
		pipelineID:       pipelineID,
		notifier:         opts.Notifier,
		targetJobs:       opts.TargetJobs,
		webhookData:      opts.WebhookData,
		dedupTTL:         opts.DedupTTL,
		secretsManager:   opts.SecretsManager,
		resourceRegistry: registry,
		agentBaseURLs:    opts.AgentBaseURLs,
		outputCallback:   opts.OutputCallback,
	}
}

// buildDependentsIndex builds a reverse lookup from job name to jobs that depend on it
// via passed constraints. Called once at the start of Run() to avoid O(N²) scanning.
func (r *Runner) buildDependentsIndex() {
	r.dependents = make(map[string][]*config.Job, len(r.config.Jobs))

	for i := range r.config.Jobs {
		job := &r.config.Jobs[i]

		for _, step := range job.Plan {
			if step.Get != "" {
				for _, dep := range step.GetConfig.Passed {
					r.dependents[dep] = append(r.dependents[dep], job)
				}
			}
		}
	}
}

// Run executes all jobs respecting passed constraints and validates pipeline-level assertions.
func (r *Runner) Run(ctx context.Context) error {
	jobResults := make(map[string]bool)
	var executedJobs []string

	r.initNotifier()
	r.prewriteJobStates(ctx)
	r.buildDependentsIndex()

	var runJob func(job *config.Job) error
	runJob = func(job *config.Job) error {
		if _, done := jobResults[job.Name]; done {
			return nil
		}

		jr := newJobRunner(job, r.driver, r.storage, r.logger, r.runID, r.pipelineID, r.config.Resources, r.config.ResourceTypes, r.config.MaxInFlight, r.notifier, r.webhookData, r.dedupTTL, r.secretsManager, r.resourceRegistry, r.agentBaseURLs, r.outputCallback)

		err := jr.Run(ctx)
		if err != nil {
			jobResults[job.Name] = false

			return fmt.Errorf("job %q: %w", job.Name, err)
		}

		jobResults[job.Name] = true
		executedJobs = append(executedJobs, job.Name)

		for _, depJob := range r.findDependentJobs(job.Name) {
			if r.canJobRun(ctx, depJob, jobResults) {
				err := runJob(depJob)
				if err != nil {
					return err
				}
			}
		}

		return nil
	}

	if len(r.targetJobs) > 0 {
		err := r.runTargetedJobs(ctx, runJob, jobResults)
		if err != nil {
			return err
		}
	} else {
		for i := range r.config.Jobs {
			job := &r.config.Jobs[i]

			if r.canJobRun(ctx, job, jobResults) {
				err := runJob(job)
				if err != nil {
					return err
				}
			}
		}
	}

	err := r.validateAssertions(executedJobs)
	if err != nil {
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
// Uses the pre-built dependents index for O(1) lookup instead of O(N×S×P) scan.
func (r *Runner) findDependentJobs(jobName string) []*config.Job {
	return r.dependents[jobName]
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

func (r *Runner) computeBlockedBy(ctx context.Context, job *config.Job) []map[string]string {
	var blockedBy []map[string]string

	for _, step := range job.Plan {
		if step.Get == "" || len(step.GetConfig.Passed) == 0 {
			continue
		}

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

			if lastStatus == "success" {
				continue
			}

			if lastStatus == "" {
				lastStatus = "never-run"
			}

			blockedBy = append(blockedBy, map[string]string{
				"job":        dep,
				"lastStatus": lastStatus,
			})
		}
	}

	return blockedBy
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

		blockedBy := r.computeBlockedBy(ctx, job)
		if len(blockedBy) > 0 {
			payload["blockedBy"] = blockedBy
		}

		jobKey := fmt.Sprintf("/pipeline/%s/jobs/%s", r.runID, job.Name)

		err := r.storage.Set(ctx, jobKey, payload)
		if err != nil {
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

			err := r.storage.Set(ctx, jobKey, storage.Payload{
				"status": "skipped",
				"reason": "passed constraints not satisfied",
			})
			if err != nil {
				r.logger.Warn("targeted.skip.failed",
					slog.String("job", job.Name),
					slog.Any("error", err),
				)
			}

			continue
		}

		err := runJob(job)
		if err != nil {
			return err
		}
	}

	// Mark remaining non-executed jobs as skipped.
	for i := range r.config.Jobs {
		job := &r.config.Jobs[i]

		if _, executed := jobResults[job.Name]; !executed {
			jobKey := fmt.Sprintf("/pipeline/%s/jobs/%s", r.runID, job.Name)

			err := r.storage.Set(ctx, jobKey, storage.Payload{
				"status": "skipped",
			})
			if err != nil {
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
