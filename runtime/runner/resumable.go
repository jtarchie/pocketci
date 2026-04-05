package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	storagelib "github.com/jtarchie/pocketci/storage"
)

// ResumableRunner wraps PipelineRunner with state persistence and resume capability.
type ResumableRunner struct {
	runner    *PipelineRunner
	storage   storagelib.Driver
	state     *PipelineState
	logger    *slog.Logger
	ctx       context.Context //nolint: containedctx
	client    orchestra.Driver
	callIndex int // Tracks how many times Run() has been called this session
}

// ResumeOptions configures resume behavior.
type ResumeOptions struct {
	// RunID is the unique identifier for this pipeline run.
	// If resuming, this should match the previous run's ID.
	RunID string
	// Resume indicates whether to attempt resuming a previous run.
	Resume bool
}

// NewResumableRunner creates a new resumable runner.
func NewResumableRunner(
	ctx context.Context,
	client orchestra.Driver,
	store storagelib.Driver,
	logger *slog.Logger,
	namespace string,
	opts ResumeOptions,
) (*ResumableRunner, error) {
	runID := opts.RunID
	if runID == "" {
		runID = support.UniqueID()
	}

	runner := NewPipelineRunner(ctx, client, store, logger, namespace, runID)

	resumableLogger := logger.WithGroup("resumable.runner").With("runID", runID)

	r := &ResumableRunner{
		runner:  runner,
		storage: store,
		logger:  resumableLogger,
		ctx:     ctx,
		client:  client,
	}

	// Try to load existing state if resuming
	if opts.Resume {
		state, err := r.loadState(runID)
		if err != nil && !errors.Is(err, storagelib.ErrNotFound) {
			return nil, fmt.Errorf("could not load pipeline state: %w", err)
		}
		if state != nil {
			r.state = state
			resumableLogger.Info("resume.loaded_state",
				"stepCount", len(state.Steps),
				"inProgress", len(state.InProgressSteps()),
			)
		}
	}

	// Create new state if not resuming or no state found
	if r.state == nil {
		r.state = NewPipelineState(runID, opts.Resume)
	}

	return r, nil
}

const stateStoragePrefix = "_resume/state"

// SetSecretsManager configures the underlying pipeline runner to load secrets.
func (r *ResumableRunner) SetSecretsManager(mgr secrets.Manager, pipelineID string) {
	r.runner.SetSecretsManager(mgr, pipelineID)
}

// loadState loads pipeline state from storage.
func (r *ResumableRunner) loadState(runID string) (*PipelineState, error) {
	payload, err := r.storage.Get(r.ctx, stateStoragePrefix+"/"+runID)
	if err != nil {
		return nil, err
	}

	// Serialize payload to JSON and back to PipelineState
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("could not marshal payload: %w", err)
	}

	var state PipelineState
	if err := json.Unmarshal(jsonBytes, &state); err != nil {
		return nil, fmt.Errorf("could not unmarshal state: %w", err)
	}

	return &state, nil
}

// saveState persists the current pipeline state.
func (r *ResumableRunner) saveState() error {
	return r.storage.Set(r.ctx, stateStoragePrefix+"/"+r.state.RunID, r.state)
}

// Run executes a task with resume support.
// If the task was previously completed, returns the cached result.
// If the task was previously failed/aborted, re-runs it.
// If the task was in progress and the container is still running, reattaches.
// Otherwise, starts a new container.
func (r *ResumableRunner) Run(input RunInput) (*RunResult, error) {
	// First, try to find an existing step by looking at steps in order
	// This allows for resuming even if step names are the same
	stepID := r.findOrGenerateStepID(input.Name)

	// Check if this step already exists in state
	existingStep := r.state.GetStep(stepID)

	if existingStep == nil {
		// Run the step fresh
		return r.runStep(stepID, input)
	}

	// Handle completed step - skip and return cached result
	if existingStep.CanSkip() {
		r.logger.Info("resume.skip_completed", "stepID", stepID, "name", input.Name)
		return existingStep.Result, nil
	}

	// Handle failed/aborted step - retry
	if existingStep.ShouldRetry() {
		r.logger.Info("resume.retry_step", "stepID", stepID, "name", input.Name, "previousStatus", existingStep.Status)
		existingStep.MarkForRetry()
		err := r.saveState()
		if err != nil {
			r.logger.Error("resume.save_state_failed.retry", "stepID", stepID, "err", err)
		}
		// Fall through to run fresh
	}

	// Handle step that was in progress - try to reattach
	if existingStep.IsResumable() {
		r.logger.Info("resume.reattach_attempt", "stepID", stepID, "containerID", existingStep.ContainerID)
		result, err := r.reattachToContainer(existingStep)
		if err == nil {
			return result, nil
		}
		r.logger.Warn("resume.reattach_failed", "stepID", stepID, "err", err)
		// Fall through to run new container
	}

	// Run the step fresh
	return r.runStep(stepID, input)
}

// findOrGenerateStepID finds an existing step ID or generates a new one.
// For resuming, we need to match steps by their position in the pipeline.
func (r *ResumableRunner) findOrGenerateStepID(name string) string {
	sanitizedName := sanitizeName(name)
	stepID := fmt.Sprintf("%d-%s", r.callIndex, sanitizedName)
	r.callIndex++ // Increment for next call
	return stepID
}

// sanitizeName makes a name safe for use in IDs.
func sanitizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	// Limit length
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}

// runStep executes a step and persists its state.
func (r *ResumableRunner) runStep(stepID string, input RunInput) (*RunResult, error) {
	now := time.Now()
	ctx := r.ctx

	// Handle timeout
	if input.Timeout != "" {
		timeout, err := time.ParseDuration(input.Timeout)
		if err != nil {
			return nil, fmt.Errorf("could not parse timeout: %w", err)
		}

		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}

	// Create task ID for container tracking (deterministic for consistency across resumes)
	taskID := support.DeterministicTaskID(r.runner.namespace, r.state.RunID, stepID, input.Name)

	// Create and persist step state as running
	step := &StepState{
		StepID:    stepID,
		Name:      input.Name,
		Kind:      StepKindRun,
		Status:    StepStatusRunning,
		TaskID:    taskID,
		StartedAt: &now,
	}
	r.state.SetStep(step)
	if err := r.saveState(); err != nil {
		r.logger.Error("resume.save_state_failed.running", "stepID", stepID, "err", err)
	}

	container, err := r.startStepContainer(ctx, stepID, taskID, input)
	if err != nil {
		return r.handleStepContainerError(step, err)
	}

	// Update step with container ID for potential reattachment
	step.ContainerID = container.ID()
	if err := r.saveState(); err != nil {
		r.logger.Error("resume.save_state_failed.container_set", "stepID", stepID, "err", err)
	}

	result, err := r.waitAndCollectStepResult(ctx, container, step)
	if err != nil {
		return nil, err
	}

	if err := r.saveState(); err != nil {
		r.logger.Error("resume.save_state_failed.completed", "stepID", stepID, "err", err)
	}

	cleanupErr := container.Cleanup(ctx)
	if cleanupErr != nil {
		r.logger.Error("container.cleanup", "err", cleanupErr)
	}

	return result, nil
}

// startStepContainer builds mounts/command from input and runs the container.
func (r *ResumableRunner) startStepContainer(ctx context.Context, stepID, taskID string, input RunInput) (orchestra.Container, error) {
	mounts := make(orchestra.Mounts, 0, len(input.Mounts))
	for path, volume := range input.Mounts {
		mounts = append(mounts, orchestra.Mount{
			Name: volume.Name,
			Path: path,
		})
	}

	command := make([]string, 0, 1+len(input.Command.Args))
	command = append(command, input.Command.Path)
	command = append(command, input.Command.Args...)

	var stdinReader io.Reader
	if input.Stdin != "" {
		stdinReader = strings.NewReader(input.Stdin)
	}

	return r.client.RunContainer(
		ctx,
		orchestra.Task{
			Command: command,
			ContainerLimits: orchestra.ContainerLimits{
				CPU:    input.ContainerLimits.CPU,
				Memory: input.ContainerLimits.Memory,
			},
			Env:        input.Env,
			ID:         fmt.Sprintf("%s-%s", input.Name, taskID),
			Image:      input.Image,
			Mounts:     mounts,
			Privileged: input.Privileged,
			Stdin:      stdinReader,
			User:       input.Command.User,
		},
	)
}

// handleStepContainerError updates step state based on the container creation error.
func (r *ResumableRunner) handleStepContainerError(step *StepState, err error) (*RunResult, error) {
	r.logger.Error("container.run.create_failed", "err", err)

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		step.Status = StepStatusAborted
		_ = r.saveState()
		return &RunResult{Status: RunAbort}, nil
	}

	step.Status = StepStatusFailed
	step.Error = err.Error()
	_ = r.saveState()
	return nil, fmt.Errorf("could not run container: %w", err)
}

// waitAndCollectStepResult waits for the container to finish, collects logs,
// and updates step state with the result.
func (r *ResumableRunner) waitAndCollectStepResult(ctx context.Context, container orchestra.Container, step *StepState) (*RunResult, error) {
	containerStatus, err := r.waitForContainer(ctx, container)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			step.Status = StepStatusAborted
			_ = r.saveState()
			cleanupErr := container.Cleanup(context.Background()) //nolint:contextcheck // cleanup after cancellation needs fresh context
			if cleanupErr != nil {
				r.logger.Error("container.cleanup.abort", "err", cleanupErr)
			}
			return &RunResult{Status: RunAbort}, nil
		}
		step.Status = StepStatusFailed
		step.Error = err.Error()
		_ = r.saveState()
		return nil, fmt.Errorf("could not get container status: %w", err)
	}

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	if err := container.Logs(ctx, stdout, stderr, false); err != nil {
		r.logger.Error("container.logs.failed", "err", err)

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			step.Status = StepStatusAborted
			_ = r.saveState()
			cleanupErr := container.Cleanup(context.Background()) //nolint:contextcheck // cleanup after cancellation needs fresh context
			if cleanupErr != nil {
				r.logger.Error("container.cleanup.abort", "err", cleanupErr)
			}
			return &RunResult{Status: RunAbort}, nil
		}

		step.Status = StepStatusFailed
		step.Error = err.Error()
		_ = r.saveState()
		return nil, fmt.Errorf("could not get container logs: %w", err)
	}

	result := &RunResult{
		Status: RunComplete,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Code:   containerStatus.ExitCode(),
	}

	completedAt := time.Now()
	step.CompletedAt = &completedAt
	step.Status = StepStatusCompleted
	step.Result = result
	if result.Code != 0 {
		step.ExitCode = &result.Code
	}

	return result, nil
}

// waitForContainer polls container status until it is done.
func (r *ResumableRunner) waitForContainer(ctx context.Context, container orchestra.Container) (orchestra.ContainerStatus, error) {
	for {
		status, err := container.Status(ctx)
		if err != nil {
			return status, err
		}

		if status.IsDone() {
			return status, nil
		}
	}
}

// reattachToContainer attempts to reattach to an existing container.
func (r *ResumableRunner) reattachToContainer(step *StepState) (*RunResult, error) {
	container, err := r.client.GetContainer(r.ctx, step.ContainerID)
	if err != nil {
		if errors.Is(err, orchestra.ErrContainerNotFound) {
			return nil, fmt.Errorf("container no longer exists: %w", err)
		}
		return nil, fmt.Errorf("could not get container: %w", err)
	}

	// Wait for container to complete
	var containerStatus orchestra.ContainerStatus
	for {
		containerStatus, err = container.Status(r.ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return &RunResult{Status: RunAbort}, nil
			}
			return nil, fmt.Errorf("could not get container status: %w", err)
		}

		if containerStatus.IsDone() {
			break
		}
	}

	// Get logs
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	err = container.Logs(r.ctx, stdout, stderr, false)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &RunResult{Status: RunAbort}, nil
		}
		return nil, fmt.Errorf("could not get container logs: %w", err)
	}

	// Update step state
	completedAt := time.Now()
	step.CompletedAt = &completedAt
	exitCode := containerStatus.ExitCode()
	step.ExitCode = &exitCode
	step.Status = StepStatusCompleted
	step.Result = &RunResult{
		Status: RunComplete,
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Code:   exitCode,
	}

	if err := r.saveState(); err != nil {
		r.logger.Error("resume.save_state_failed.reattach", "stepID", step.StepID, "err", err)
	}

	// Clean up container
	if err := container.Cleanup(r.ctx); err != nil {
		r.logger.Error("resume.container.cleanup.failed", "stepID", step.StepID, "err", err)
	}

	return step.Result, nil
}

// CreateVolume creates a volume with resume support.
// On resume, previously-created volumes are tracked so the same names are reused.
func (r *ResumableRunner) CreateVolume(input VolumeInput) (*VolumeResult, error) {
	result, err := r.runner.CreateVolume(input)
	if err != nil {
		return nil, err
	}

	// Track volume in state for resume awareness
	if r.state.Volumes == nil {
		r.state.Volumes = make(map[string]*VolumeState)
	}

	r.state.Volumes[input.Name] = &VolumeState{
		Name: result.Name,
		Path: result.Path,
	}

	saveErr := r.saveState()
	if saveErr != nil {
		r.logger.Error("resume.save_state_failed.volume", "name", input.Name, "err", saveErr)
	}

	return result, nil
}

// CleanupVolumes cleans up all tracked volumes (passthrough to underlying runner).
func (r *ResumableRunner) CleanupVolumes() error {
	return r.runner.CleanupVolumes()
}

// ReadFilesFromVolume delegates to the underlying pipeline runner.
func (r *ResumableRunner) ReadFilesFromVolume(volumeName string, filePaths ...string) (map[string]string, error) {
	return r.runner.ReadFilesFromVolume(volumeName, filePaths...)
}

// SetPreseededVolumes configures the underlying pipeline runner to reuse pre-created volumes.
func (r *ResumableRunner) SetPreseededVolumes(vols map[string]orchestra.Volume) {
	r.runner.SetPreseededVolumes(vols)
}

// SetOutputCallback configures the underlying pipeline runner's global output callback.
func (r *ResumableRunner) SetOutputCallback(cb OutputCallback) {
	r.runner.SetOutputCallback(cb)
}

// SetAgentFunc configures the function used to execute agent steps.
func (r *ResumableRunner) SetAgentFunc(fn AgentFunc) {
	r.runner.SetAgentFunc(fn)
}

// RunAgent executes an LLM agent step with resume support.
// Completed agents are skipped; failed/aborted agents are retried from scratch.
func (r *ResumableRunner) RunAgent(configJSON json.RawMessage) (json.RawMessage, error) {
	// Extract name from config for step ID generation
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(configJSON, &meta); err != nil {
		return nil, fmt.Errorf("could not parse agent config name: %w", err)
	}

	name := meta.Name
	if name == "" {
		name = "agent"
	}

	stepID := r.findOrGenerateStepID(name)
	existingStep := r.state.GetStep(stepID)

	// Completed agent — return cached result
	if existingStep != nil && existingStep.CanSkip() {
		r.logger.Info("resume.skip_completed_agent", "stepID", stepID, "name", name)
		return existingStep.AgentResultJSON, nil
	}

	// Failed/aborted agent — retry from scratch
	if existingStep != nil && existingStep.ShouldRetry() {
		r.logger.Info("resume.retry_agent", "stepID", stepID, "name", name, "previousStatus", existingStep.Status)
		existingStep.MarkForRetry()
		err := r.saveState()
		if err != nil {
			r.logger.Error("resume.save_state_failed.agent_retry", "stepID", stepID, "err", err)
		}
	}

	// Running agent (interrupted mid-conversation) — treat as needing re-run
	if existingStep != nil && existingStep.Status == StepStatusRunning {
		r.logger.Info("resume.agent_was_running", "stepID", stepID, "name", name)
		existingStep.MarkForRetry()
		err := r.saveState()
		if err != nil {
			r.logger.Error("resume.save_state_failed.agent_running", "stepID", stepID, "err", err)
		}
	}

	// Execute agent fresh
	now := time.Now()
	step := &StepState{
		StepID:    stepID,
		Name:      name,
		Kind:      StepKindAgent,
		Status:    StepStatusRunning,
		StartedAt: &now,
	}
	r.state.SetStep(step)

	if err := r.saveState(); err != nil {
		r.logger.Error("resume.save_state_failed.agent_running", "stepID", stepID, "err", err)
	}

	resultJSON, err := r.runner.RunAgent(configJSON)
	if err != nil {
		step.Status = StepStatusFailed
		step.Error = err.Error()
		_ = r.saveState()

		return nil, err
	}

	// Persist completed result
	completedAt := time.Now()
	step.CompletedAt = &completedAt
	step.Status = StepStatusCompleted
	step.AgentResultJSON = resultJSON

	if err := r.saveState(); err != nil {
		r.logger.Error("resume.save_state_failed.agent_completed", "stepID", stepID, "err", err)
	}

	return resultJSON, nil
}

// MarkInProgressAsAborted marks all currently-running steps as aborted.
// Called during context cancellation cleanup to ensure state is consistent.
func (r *ResumableRunner) MarkInProgressAsAborted() {
	for _, step := range r.state.InProgressSteps() {
		step.Status = StepStatusAborted
		step.Error = "context cancelled"
	}

	err := r.saveState()
	if err != nil {
		r.logger.Error("resume.save_state_failed.abort_cleanup", "err", err)
	}
}

// State returns the current pipeline state.
func (r *ResumableRunner) State() *PipelineState {
	return r.state
}
