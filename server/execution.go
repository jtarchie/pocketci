package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
)

// ErrRunNotInFlight is returned when a stop is requested for a run that is not currently executing.
var ErrRunNotInFlight = errors.New("run is not currently in flight")

// ErrQueueFull is returned when the execution queue has reached its maximum size.
var ErrQueueFull = errors.New("execution queue is full")

// ExecutionService manages pipeline execution with concurrency limits and queuing.
type ExecutionService struct {
	store                 storage.Driver
	logger                *slog.Logger
	maxInFlight           int
	inFlight              atomic.Int32
	wg                    sync.WaitGroup
	DefaultDriver         string
	DriverConfigs         map[string]orchestra.DriverConfig
	CacheStore            cache.CacheStore
	CacheCompression      string
	CacheKeyPrefix        string
	SecretsManager        secrets.Manager
	AllowedFeatures       []Feature
	FetchTimeout          time.Duration
	FetchMaxResponseBytes int64
	DedupTTL              time.Duration
	stopRegistry          map[string]context.CancelFunc
	stopMu                sync.Mutex

	// Queue fields
	maxQueueSize    int
	cond            *sync.Cond
	queueDone       atomic.Bool
	queueWg         sync.WaitGroup
	pendingWebhooks map[string]chan *jsapi.HTTPResponse
	pendingMu       sync.Mutex
}

// NewExecutionService creates a new execution service.
// The allowedDrivers list determines the default driver (first in list).
// If allowedDrivers is empty or contains "*", defaults to "docker".
// maxQueueSize controls the maximum number of pending queued runs; 0 disables queuing.
func NewExecutionService(store storage.Driver, logger *slog.Logger, maxInFlight int, maxQueueSize int, allowedDrivers []string) *ExecutionService {
	if maxInFlight <= 0 {
		maxInFlight = 10 // default limit
	}

	// Determine default driver: first allowed driver, or "docker" if wildcard/empty
	defaultDriver := "docker"
	if len(allowedDrivers) > 0 && allowedDrivers[0] != "*" {
		defaultDriver = allowedDrivers[0]
	}

	svc := &ExecutionService{
		store:           store,
		logger:          logger.WithGroup("executor.run"),
		maxInFlight:     maxInFlight,
		maxQueueSize:    maxQueueSize,
		DefaultDriver:   defaultDriver,
		stopRegistry:    make(map[string]context.CancelFunc),
		pendingWebhooks: make(map[string]chan *jsapi.HTTPResponse),
	}
	svc.cond = sync.NewCond(&sync.Mutex{})

	if maxQueueSize > 0 {
		svc.queueWg.Add(1)

		go svc.processQueue()
	}

	return svc
}

// Wait blocks until all in-flight pipeline executions have completed.
// This is useful for graceful shutdown or testing.
func (s *ExecutionService) Wait() {
	s.wg.Wait()
}

// Shutdown stops the queue processor and waits for all in-flight executions to complete.
func (s *ExecutionService) Shutdown() {
	s.queueDone.Store(true)
	s.cond.Broadcast()
	s.queueWg.Wait()
	s.wg.Wait()
}

// processQueue is a background goroutine that dispatches queued runs as in-flight slots free up.
func (s *ExecutionService) processQueue() {
	defer s.queueWg.Done()

	for {
		s.cond.L.Lock()
		for !s.CanExecute() && !s.queueDone.Load() {
			s.cond.Wait()
		}
		s.cond.L.Unlock()

		ctx := context.Background()

		runs, err := s.store.GetRecentRunsByStatus(ctx, storage.RunStatusQueued, s.maxInFlight)
		if err != nil {
			s.logger.Error("queue.processor.list_failed", "error", err)

			if s.queueDone.Load() {
				return
			}

			continue
		}

		if len(runs) == 0 {
			if s.queueDone.Load() {
				return
			}

			// Brief sleep to avoid a tight spin if Broadcast is called
			// but no runs are actually queued yet (race between SaveRun and Broadcast).
			time.Sleep(50 * time.Millisecond)

			continue
		}

		for i := range runs {
			if !s.CanExecute() {
				break
			}

			s.dispatchQueuedRun(ctx, &runs[i])
		}
	}
}

// dispatchQueuedRun resolves a queued run's pipeline and options, then dispatches it.
func (s *ExecutionService) dispatchQueuedRun(ctx context.Context, run *storage.PipelineRun) {
	logger := s.logger.With("run_id", run.ID, "pipeline_id", run.PipelineID)

	pipeline, pErr := s.store.GetPipeline(ctx, run.PipelineID)
	if pErr != nil {
		logger.Error("queue.processor.get_pipeline_failed", "error", pErr)
		_ = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "Pipeline not found during queue dispatch")

		return
	}

	opts := s.buildQueuedExecOptions(run)
	s.dispatchRun(pipeline, run, opts) //nolint:contextcheck // deliberate: execution goroutine creates its own context
}

// buildQueuedExecOptions constructs execOptions for a run being dispatched from the queue.
func (s *ExecutionService) buildQueuedExecOptions(run *storage.PipelineRun) execOptions {
	// Look up any pending webhook response channel for this run.
	s.pendingMu.Lock()
	responseChan, hasWebhook := s.pendingWebhooks[run.ID]
	delete(s.pendingWebhooks, run.ID)
	s.pendingMu.Unlock()

	opts := execOptions{}
	if run.TriggerType == storage.TriggerTypeResume {
		opts.resume = true
	}

	if run.TriggerInput.Args != nil {
		opts.args = run.TriggerInput.Args
	}

	if len(run.TriggerInput.Jobs) > 0 {
		opts.jobs = run.TriggerInput.Jobs
	}

	if hasWebhook && run.TriggerInput.Webhook != nil {
		opts.webhook = &webhookExecData{
			webhookData: &jsapi.WebhookData{
				Provider:  run.TriggerInput.Webhook.Provider,
				EventType: run.TriggerInput.Webhook.EventType,
				Method:    run.TriggerInput.Webhook.Method,
				URL:       run.TriggerInput.Webhook.URL,
				Headers:   run.TriggerInput.Webhook.Headers,
				Body:      run.TriggerInput.Webhook.Body,
				Query:     run.TriggerInput.Webhook.Query,
			},
			responseChan: responseChan,
		}
	}

	return opts
}

// dispatchRun starts an in-flight execution for a queued run.
func (s *ExecutionService) dispatchRun(pipeline *storage.Pipeline, run *storage.PipelineRun, opts execOptions) {
	s.inFlight.Add(1)
	s.wg.Add(1)

	go s.executePipeline(pipeline, run, opts)
}

// signalSlotFreed wakes the queue processor after an in-flight slot is released.
func (s *ExecutionService) signalSlotFreed() {
	s.cond.Broadcast()
}

// CanAccept returns true if a new pipeline can be started immediately or queued.
func (s *ExecutionService) CanAccept(ctx context.Context) bool {
	if s.CanExecute() {
		return true
	}

	return s.maxQueueSize > 0 && s.QueueLength(ctx) < s.maxQueueSize
}

// QueueLength returns the number of runs currently in queued status.
func (s *ExecutionService) QueueLength(ctx context.Context) int {
	runs, err := s.store.GetRunsByStatus(ctx, storage.RunStatusQueued)
	if err != nil {
		return 0
	}

	return len(runs)
}

// MaxQueueSize returns the configured maximum queue size.
func (s *ExecutionService) MaxQueueSize() int {
	return s.maxQueueSize
}

// StopRun cancels an in-flight pipeline execution by its run ID.
// Returns ErrRunNotInFlight if the run is not currently executing.
func (s *ExecutionService) StopRun(runID string) error {
	s.stopMu.Lock()
	cancel, ok := s.stopRegistry[runID]
	if !ok {
		s.stopMu.Unlock()

		return ErrRunNotInFlight
	}

	s.stopMu.Unlock()

	cancel()

	// Force the run to "failed" in the DB. If the execution goroutine already
	// committed a terminal status (e.g. "success") before observing the
	// cancellation, this overwrites it. The goroutine may also write "failed"
	// when it sees ctx.Err() — the double-write is harmless and idempotent.
	dbCtx := context.Background()

	_ = s.store.UpdateStatusForPrefix(dbCtx, "/pipeline/"+runID+"/", []string{"pending", "running"}, "aborted")
	_ = s.store.UpdateRunStatus(dbCtx, runID, storage.RunStatusFailed, "Run stopped by user")

	return nil
}

// CanExecute returns true if a new pipeline can be started.
func (s *ExecutionService) CanExecute() bool {
	return int(s.inFlight.Load()) < s.maxInFlight
}

// CurrentInFlight returns the number of currently running pipelines.
func (s *ExecutionService) CurrentInFlight() int {
	return int(s.inFlight.Load())
}

// MaxInFlight returns the maximum number of concurrent pipelines allowed.
func (s *ExecutionService) MaxInFlight() int {
	return s.maxInFlight
}

// TriggerPipeline starts a new pipeline execution asynchronously.
// It creates a run record, starts a goroutine to execute the pipeline,
// and returns the run ID immediately. Optional args are passed through
// to pipelineContext.args in the runtime.
// Returns ErrQueueFull if both in-flight slots and the queue are at capacity.
func (s *ExecutionService) TriggerPipeline(ctx context.Context, pipeline *storage.Pipeline, args []string) (*storage.PipelineRun, error) {
	requestID, _ := RequestIDFromContext(ctx)
	actor, _ := RequestActorFromContext(ctx)

	// Check if we can accept this run (either execute or queue it)
	if !s.CanAccept(ctx) {
		return nil, ErrQueueFull
	}

	// Create run record with queued status
	run, err := s.store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, formatTriggeredBy(actor), storage.TriggerInput{Args: args})
	if err != nil {
		return nil, err
	}

	opts := execOptions{args: args, requestID: requestID, authProvider: actor.Provider, user: actor.User}

	// Fast path: dispatch immediately if a slot is available
	if s.CanExecute() {
		s.dispatchRun(pipeline, run, opts) //nolint:contextcheck // deliberate: execution goroutine creates its own context

		return run, nil
	}

	// Slow path: run stays in queued status in DB, wake the processor
	s.logger.Info("queue.enqueued", "run_id", run.ID, "pipeline_id", pipeline.ID)
	s.cond.Broadcast()

	return run, nil
}

// TriggerWebhookPipeline starts a new pipeline execution triggered by a webhook.
// It passes webhook request data and a response channel through to the pipeline runtime.
// Returns ErrQueueFull if both in-flight slots and the queue are at capacity.
func (s *ExecutionService) TriggerWebhookPipeline(
	ctx context.Context,
	pipeline *storage.Pipeline,
	webhookData *jsapi.WebhookData,
	responseChan chan *jsapi.HTTPResponse,
) (*storage.PipelineRun, error) {
	requestID, _ := RequestIDFromContext(ctx)
	actor, _ := RequestActorFromContext(ctx)

	// Check if we can accept this run (either execute or queue it)
	if !s.CanAccept(ctx) {
		return nil, ErrQueueFull
	}

	// Create run record with queued status
	triggeredBy := formatWebhookTriggeredBy(webhookData)
	triggerInput := storage.TriggerInput{Webhook: toTriggerWebhookInput(webhookData)}

	run, err := s.store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeWebhook, triggeredBy, triggerInput)
	if err != nil {
		return nil, err
	}

	opts := execOptions{
		webhook: &webhookExecData{
			webhookData:  webhookData,
			responseChan: responseChan,
		},
		requestID:    requestID,
		authProvider: actor.Provider,
		user:         actor.User,
	}

	// Fast path: dispatch immediately if a slot is available
	if s.CanExecute() {
		s.dispatchRun(pipeline, run, opts) //nolint:contextcheck // deliberate: execution goroutine creates its own context

		return run, nil
	}

	// Slow path: store response channel for later dispatch, wake the processor
	s.pendingMu.Lock()
	s.pendingWebhooks[run.ID] = responseChan
	s.pendingMu.Unlock()

	s.logger.Info("queue.enqueued", "run_id", run.ID, "pipeline_id", pipeline.ID)
	s.cond.Broadcast()

	return run, nil
}

// ResumePipeline resumes a failed or aborted pipeline run.
// It reuses the existing run ID so that the ResumableRunner can load
// previous state and skip completed steps.
// Returns ErrQueueFull if both in-flight slots and the queue are at capacity.
func (s *ExecutionService) ResumePipeline(ctx context.Context, pipeline *storage.Pipeline, run *storage.PipelineRun) error {
	// Check if we can accept this run (either execute or queue it)
	if !s.CanAccept(ctx) {
		return ErrQueueFull
	}

	requestID, _ := RequestIDFromContext(ctx)
	actor, _ := RequestActorFromContext(ctx)

	// Reset run status to queued for the resumed execution
	if err := s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusQueued, ""); err != nil {
		return fmt.Errorf("failed to reset run status: %w", err)
	}

	opts := execOptions{resume: true, requestID: requestID, authProvider: actor.Provider, user: actor.User}

	// Fast path: dispatch immediately if a slot is available
	if s.CanExecute() {
		s.dispatchRun(pipeline, run, opts) //nolint:contextcheck // deliberate: execution goroutine creates its own context

		return nil
	}

	// Slow path: run stays in queued status in DB, wake the processor
	s.logger.Info("queue.enqueued", "run_id", run.ID, "pipeline_id", run.PipelineID)
	s.cond.Broadcast()

	return nil
}

// TriggerScheduledPipeline starts a pipeline execution triggered by the scheduler.
// If jobName is non-empty, only that job (and its downstream dependents) runs.
// Returns ErrQueueFull if both in-flight slots and the queue are at capacity.
func (s *ExecutionService) TriggerScheduledPipeline(ctx context.Context, pipeline *storage.Pipeline, scheduleName, jobName string) (*storage.PipelineRun, error) {
	if !s.CanAccept(ctx) {
		return nil, ErrQueueFull
	}

	triggerInput := storage.TriggerInput{}
	if jobName != "" {
		triggerInput.Jobs = []string{jobName}
	}

	triggeredBy := "schedule:" + scheduleName

	run, err := s.store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeSchedule, triggeredBy, triggerInput)
	if err != nil {
		return nil, err
	}

	opts := execOptions{jobs: triggerInput.Jobs}

	if s.CanExecute() {
		s.dispatchRun(pipeline, run, opts) //nolint:contextcheck // deliberate: execution goroutine creates its own context

		return run, nil
	}

	s.logger.Info("queue.enqueued", "run_id", run.ID, "pipeline_id", pipeline.ID, "schedule", scheduleName)
	s.cond.Broadcast()

	return run, nil
}

// RecoverOrphanedRuns handles runs that were in-flight when the server stopped.
// If the resume feature is enabled, resume-enabled pipelines are restarted;
// otherwise all orphaned runs are marked as failed and their resources cleaned up.
func (s *ExecutionService) RecoverOrphanedRuns(ctx context.Context) {
	runs, err := s.store.GetRunsByStatus(ctx, storage.RunStatusRunning)
	if err != nil {
		s.logger.Error("orphan.recovery.list_failed", "error", err)
		return
	}

	resumeEnabled := IsFeatureEnabled(FeatureResume, s.AllowedFeatures)

	for _, run := range runs {
		logger := s.logger.With("run_id", run.ID, "pipeline_id", run.PipelineID)

		pipeline, pErr := s.store.GetPipeline(ctx, run.PipelineID)
		if pErr != nil {
			logger.Error("orphan.recovery.get_pipeline_failed", "error", pErr)
			_ = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "Server restarted; pipeline not found")
			continue
		}

		if resumeEnabled && pipeline.ResumeEnabled {
			logger.Info("orphan.recovery.resuming")
			runCopy := run
			if rErr := s.ResumePipeline(ctx, pipeline, &runCopy); rErr != nil {
				logger.Error("orphan.recovery.resume_failed", "error", rErr)
				_ = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "Server restarted; resume failed: "+rErr.Error())
				s.cleanupOrphanedRunResources(ctx, run.ID, pipeline, logger)
			}
			continue
		}

		logger.Info("orphan.recovery.marking_failed")
		_ = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "Server restarted during execution")
		_ = s.store.UpdateStatusForPrefix(ctx, "/pipeline/"+run.ID+"/", []string{"pending", "running"}, "aborted")
		s.cleanupOrphanedRunResources(ctx, run.ID, pipeline, logger)
	}

	// Wake the queue processor to dispatch any runs that were queued before the restart.
	s.cond.Broadcast()
}

// cleanupOrphanedRunResources destroys any containers and volumes left behind
// by a run that was interrupted when the server stopped.
func (s *ExecutionService) cleanupOrphanedRunResources(ctx context.Context, runID string, pipeline *storage.Pipeline, logger *slog.Logger) {
	namespace := "ci-" + runID
	factory := s.resolveDriverFactory(ctx, pipeline, logger)

	driver, err := factory(ctx, namespace)
	if err != nil {
		logger.Warn("orphan.recovery.cleanup_driver_failed", "namespace", namespace, "error", err)
		return
	}

	if err := driver.Close(); err != nil {
		logger.Warn("orphan.recovery.cleanup_failed", "namespace", namespace, "error", err)
		return
	}

	logger.Info("orphan.recovery.cleanup_done", "namespace", namespace)
}

// webhookExecData holds webhook-specific execution data.
type webhookExecData struct {
	webhookData  *jsapi.WebhookData
	responseChan chan *jsapi.HTTPResponse
}

// execOptions holds options for executePipeline.
type execOptions struct {
	webhook      *webhookExecData
	args         []string
	jobs         []string
	resume       bool
	requestID    string
	authProvider string
	user         string
}

func formatTriggeredBy(actor auth.RequestActor) string {
	if actor.Provider != "" && actor.User != "" {
		return actor.Provider + ":" + actor.User
	}

	if actor.User != "" {
		return actor.User
	}

	return ""
}

func formatWebhookTriggeredBy(data *jsapi.WebhookData) string {
	if data == nil {
		return ""
	}

	if data.EventType != "" {
		return data.Provider + ":" + data.EventType
	}

	return data.Provider
}

func toTriggerWebhookInput(data *jsapi.WebhookData) *storage.TriggerWebhookInput {
	if data == nil {
		return nil
	}

	return &storage.TriggerWebhookInput{
		Provider:  data.Provider,
		EventType: data.EventType,
		Method:    data.Method,
		URL:       data.URL,
		Headers:   data.Headers,
		Body:      data.Body,
		Query:     data.Query,
	}
}

// buildExecutorOptions builds the runtime.ExecutorOptions for async pipeline execution.
func (s *ExecutionService) buildExecutorOptions(pipeline *storage.Pipeline, opts execOptions) runtime.ExecutorOptions {
	execOpts := runtime.ExecutorOptions{
		RunID:        opts.requestID, // overwritten below
		PipelineID:   pipeline.ID,
		Resume:       IsFeatureEnabled(FeatureResume, s.AllowedFeatures) && (opts.resume || pipeline.ResumeEnabled),
		RequestID:    opts.requestID,
		AuthProvider: opts.authProvider,
		User:         opts.user,
		Args:         opts.args,
	}

	if IsFeatureEnabled(FeatureSecrets, s.AllowedFeatures) {
		execOpts.SecretsManager = s.SecretsManager
	}

	if opts.webhook != nil && IsFeatureEnabled(FeatureWebhooks, s.AllowedFeatures) {
		execOpts.WebhookData = opts.webhook.webhookData
		execOpts.ResponseChan = opts.webhook.responseChan
	}

	if len(opts.jobs) > 0 {
		execOpts.TargetJobs = opts.jobs
	}

	// Wire up triggerPipeline() callback for JS API.
	execOpts.TriggerCallback = func(ctx context.Context, pipelineName string, jobs []string, args []string) (string, error) {
		if !s.CanAccept(ctx) {
			return "", ErrQueueFull
		}

		pipeline, lookupErr := s.store.GetPipelineByName(ctx, pipelineName)
		if lookupErr != nil {
			return "", fmt.Errorf("pipeline %q not found: %w", pipelineName, lookupErr)
		}

		triggerInput := storage.TriggerInput{Args: args, Jobs: jobs}

		run, triggerErr := s.store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "triggerPipeline", triggerInput)
		if triggerErr != nil {
			return "", triggerErr
		}

		triggerOpts := execOptions{args: args, jobs: jobs}
		if s.CanExecute() {
			s.dispatchRun(pipeline, run, triggerOpts) //nolint:contextcheck // deliberate: execution goroutine creates its own context
		} else {
			s.cond.Broadcast()
		}

		return run.ID, nil
	}

	execOpts.DisableNotifications = !IsFeatureEnabled(FeatureNotifications, s.AllowedFeatures)
	execOpts.DisableFetch = !IsFeatureEnabled(FeatureFetch, s.AllowedFeatures)
	execOpts.FetchTimeout = s.FetchTimeout
	execOpts.FetchMaxResponseBytes = s.FetchMaxResponseBytes
	execOpts.DedupTTL = s.DedupTTL

	return execOpts
}

// finalizeExecRun handles post-execution status updates for executePipeline.
func (s *ExecutionService) finalizeExecRun(ctx, dbCtx context.Context, run *storage.PipelineRun, pipeline *storage.Pipeline, execErr error, logger *slog.Logger) {
	if ctx.Err() != nil {
		_ = s.store.UpdateStatusForPrefix(dbCtx, "/pipeline/"+run.ID+"/", []string{"pending", "running"}, "aborted")

		updateErr := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusFailed, "Run stopped by user")
		if updateErr != nil {
			logger.Error("run.update.failed.to_failed", "error", updateErr)
		}

		return
	}

	if execErr != nil {
		logger.Error("pipeline.execute.failed", "error", execErr)

		updateErr := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusFailed, execErr.Error())
		if updateErr != nil {
			logger.Error("run.update.failed.to_failed", "error", updateErr)
		}

		return
	}

	finalStatus, errMsg := s.determineRunStatus(dbCtx, run.ID, logger)

	err := s.store.UpdateRunStatus(dbCtx, run.ID, finalStatus, errMsg)
	if err != nil {
		logger.Error("run.update.failed.to_final", "error", err)
		return
	}

	switch finalStatus {
	case storage.RunStatusSuccess:
		logger.Info("pipeline.execute.success")
	case storage.RunStatusSkipped:
		logger.Info("pipeline.execute.skipped")
	default:
		logger.Info("pipeline.execute.completed_with_failures")
	}

	s.pruneOldRuns(dbCtx, pipeline, logger)
}

func (s *ExecutionService) executePipeline(pipeline *storage.Pipeline, run *storage.PipelineRun, opts execOptions) {
	defer func() {
		s.inFlight.Add(-1)
		s.signalSlotFreed()
	}()
	defer s.wg.Done()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.stopMu.Lock()
	s.stopRegistry[run.ID] = cancel
	s.stopMu.Unlock()
	defer func() {
		s.stopMu.Lock()
		delete(s.stopRegistry, run.ID)
		s.stopMu.Unlock()
	}()

	dbCtx := context.Background()

	logger := s.logger.With(
		"event", "pipeline.execute",
		"run_id", run.ID,
		"pipeline_id", pipeline.ID,
		"pipeline_name", pipeline.Name,
	)
	if opts.requestID != "" {
		logger = logger.With("request_id", opts.requestID)
	}
	if opts.authProvider != "" && opts.user != "" {
		logger = logger.With("auth_provider", opts.authProvider, "user", opts.user)
	}

	err := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusRunning, "")
	if err != nil {
		logger.Error("run.update.failed.to_running", "error", err)
		return
	}

	logger.Info("pipeline.execute.start")

	execOpts := s.buildExecutorOptions(pipeline, opts)
	execOpts.RunID = run.ID

	executableContent, err := resolveExecutableContent(pipeline)
	if err != nil {
		logger.Error("pipeline.transpile.failed", "error", err)

		updateErr := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusFailed, err.Error())
		if updateErr != nil {
			logger.Error("run.update.failed.to_failed", "error", updateErr)
		}

		return
	}

	execOpts.DriverFactory = s.resolveDriverFactory(ctx, pipeline, logger)
	execErr := runtime.ExecutePipeline(ctx, executableContent, s.store, logger, execOpts)

	s.finalizeExecRun(ctx, dbCtx, run, pipeline, execErr, logger)
}

// RunByNameSync executes a stored pipeline by name, synchronously.
// It writes SSE events (stdout, stderr lines as data events; an exit event at completion)
// to the provided http.ResponseWriter.
//
// The pipeline is looked up by exact name; ErrNotFound is returned if missing.
// Args are passed to the pipeline via pipelineContext.args.
//
// If workdirTar is non-nil, a "workdir" volume is created and seeded from the
// tar stream *before* the SSE response starts. This ensures the HTTP request
// body is fully consumed while the connection is still in request mode, which
// is required for correct behaviour through reverse proxies.
// preseedWorkdir creates a driver, volume, and seeds it from a tar stream.
// Returns the preseeded volumes map, the driver (caller must close), and any error.
func (s *ExecutionService) preseedWorkdir(ctx context.Context, factory func(context.Context, string) (orchestra.Driver, error), runID string, workdirTar io.Reader) (map[string]orchestra.Volume, orchestra.Driver, error) {
	namespace := "ci-" + runID

	driver, dErr := factory(ctx, namespace)
	if dErr != nil {
		return nil, nil, fmt.Errorf("could not create driver: %w", dErr)
	}

	vol, vErr := driver.CreateVolume(ctx, "workdir", 0)
	if vErr != nil {
		_ = driver.Close()
		return nil, nil, fmt.Errorf("could not create workdir volume: %w", vErr)
	}

	accessor, ok := driver.(cache.VolumeDataAccessor)
	if !ok {
		_ = vol.Cleanup(ctx)
		_ = driver.Close()
		return nil, nil, fmt.Errorf("driver %q does not support volume data access", driver.Name())
	}

	s.logger.Info("workdir.preseed.start")

	if cErr := accessor.CopyToVolume(ctx, vol.Name(), workdirTar); cErr != nil {
		_ = vol.Cleanup(ctx)
		_ = driver.Close()
		return nil, nil, fmt.Errorf("could not seed workdir volume: %w", cErr)
	}

	s.logger.Info("workdir.preseed.done")

	return map[string]orchestra.Volume{"workdir": vol}, driver, nil
}

// finalizeSyncRun determines final status, updates the run, prunes old runs, and writes the SSE exit event.
func (s *ExecutionService) finalizeSyncRun(ctx context.Context, run *storage.PipelineRun, pipeline *storage.Pipeline, execErr error, w http.ResponseWriter) {
	exitCode := 0
	var finalStatus storage.RunStatus
	errMsg := ""

	if execErr != nil {
		exitCode = 1
		finalStatus = storage.RunStatusFailed
		errMsg = execErr.Error()
	} else {
		var jobErrMsg string
		finalStatus, jobErrMsg = s.determineRunStatus(ctx, run.ID, s.logger)
		if finalStatus == storage.RunStatusFailed {
			exitCode = 1
			errMsg = jobErrMsg
		}
	}

	if err := s.store.UpdateRunStatus(ctx, run.ID, finalStatus, errMsg); err != nil {
		s.logger.Error("run.update.failed.to_final", "error", err)
	}

	s.pruneOldRuns(ctx, pipeline, s.logger)

	exitEvent := map[string]any{"event": "exit", "code": exitCode, "run_id": run.ID}
	if errMsg != "" {
		exitEvent["message"] = errMsg
	}

	exitData, _ := json.Marshal(exitEvent)
	fmt.Fprintf(w, "data: %s\n\n", exitData) //nolint:errcheck
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *ExecutionService) RunByNameSync(
	ctx context.Context,
	name string,
	args []string,
	workdirTar io.Reader,
	w http.ResponseWriter,
) error {
	pipeline, err := s.store.GetPipelineByName(ctx, name)
	if err != nil {
		return err
	}

	if pipeline.Paused {
		return storage.ErrPipelinePaused
	}

	actor, _ := RequestActorFromContext(ctx)

	run, err := s.store.SaveRun(ctx, pipeline.ID, storage.TriggerTypeCLI, formatTriggeredBy(actor), storage.TriggerInput{Args: args})
	if err != nil {
		return fmt.Errorf("failed to save run: %w", err)
	}

	if err = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusRunning, ""); err != nil {
		s.logger.Error("run.update.failed.to_running", "error", err)
	}

	var preseededVolumes map[string]orchestra.Volume
	var driver orchestra.Driver

	factory := s.resolveDriverFactory(ctx, pipeline, s.logger)

	if workdirTar != nil {
		var pErr error

		preseededVolumes, driver, pErr = s.preseedWorkdir(ctx, factory, run.ID, workdirTar)
		if pErr != nil {
			return pErr
		}

		defer func() { _ = driver.Close() }()
	}

	// --- SSE headers — only written after the request body is consumed ---
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	opts := runtime.ExecutorOptions{
		RunID:                 run.ID,
		PipelineID:            pipeline.ID,
		Args:                  args,
		PreseededVolumes:      preseededVolumes,
		Driver:                driver,
		DisableNotifications:  !IsFeatureEnabled(FeatureNotifications, s.AllowedFeatures),
		DisableFetch:          !IsFeatureEnabled(FeatureFetch, s.AllowedFeatures),
		FetchTimeout:          s.FetchTimeout,
		FetchMaxResponseBytes: s.FetchMaxResponseBytes,
		DedupTTL:              s.DedupTTL,
	}
	if IsFeatureEnabled(FeatureSecrets, s.AllowedFeatures) {
		opts.SecretsManager = s.SecretsManager
	}

	// Stream stdout/stderr as SSE data events while the pipeline runs.
	var writeMu sync.Mutex
	opts.OutputCallback = func(stream, data string) {
		writeMu.Lock()
		defer writeMu.Unlock()

		evt, _ := json.Marshal(map[string]string{"stream": stream, "data": data})
		fmt.Fprintf(w, "data: %s\n\n", evt) //nolint:errcheck

		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	executableContent, execContentErr := resolveExecutableContent(pipeline)
	if execContentErr != nil {
		return fmt.Errorf("could not resolve pipeline content: %w", execContentErr)
	}

	if driver == nil {
		opts.DriverFactory = factory
	}
	execErr := runtime.ExecutePipeline(ctx, executableContent, s.store, s.logger, opts)

	s.finalizeSyncRun(ctx, run, pipeline, execErr, w)

	return nil
}

// determineRunStatus checks job statuses to determine the final run status.
// It also returns an error message from the first failed job, if any.
func (s *ExecutionService) determineRunStatus(ctx context.Context, runID string, logger *slog.Logger) (storage.RunStatus, string) {
	// Query all job statuses for this run (backwards-compat Concourse YAML pipelines).
	// Note: TypeScript pipeline task statuses under /pipeline/{runID}/tasks/ are NOT
	// checked here because individual task failures don't necessarily mean the pipeline
	// failed — the pipeline may handle errors (e.g., try/catch). Pipeline-level failure
	// is already handled by the executePipeline error return path.
	prefix := "/pipeline/" + runID + "/jobs"
	results, err := s.store.GetAll(ctx, prefix, []string{"status", "errorMessage"})
	if err != nil {
		logger.Warn("failed to query job statuses, assuming success", "error", err)
		return storage.RunStatusSuccess, ""
	}

	hasStatuses := false
	allSkipped := true

	// Check if any job has a failed/error status.
	for _, result := range results {
		if status, ok := result.Payload["status"].(string); ok {
			hasStatuses = true

			switch status {
			case "failure", "error", "abort":
				errMsg, _ := result.Payload["errorMessage"].(string)
				return storage.RunStatusFailed, errMsg
			}

			if status != string(storage.RunStatusSkipped) {
				allSkipped = false
			}
		}
	}

	if hasStatuses && allSkipped {
		return storage.RunStatusSkipped, ""
	}

	return storage.RunStatusSuccess, ""
}

// pruneOldRuns enforces build_log_retention policy for YAML pipelines.
// It parses the pipeline config, finds the most restrictive retention across
// all jobs, and deletes runs that exceed the limits. Errors are non-fatal.
func (s *ExecutionService) pruneOldRuns(ctx context.Context, pipeline *storage.Pipeline, logger *slog.Logger) {
	if pipeline.ContentType != storage.ContentTypeYAML {
		return
	}

	cfg, err := backwards.ParseConfig(pipeline.Content)
	if err != nil || len(cfg.Jobs) == 0 {
		return
	}

	keepBuilds, keepDays := minRetentionLimits(cfg.Jobs)

	if keepBuilds == 0 && keepDays == 0 {
		return
	}

	var cutoff *time.Time
	if keepDays > 0 {
		t := time.Now().UTC().AddDate(0, 0, -keepDays)
		cutoff = &t
	}

	if err := s.store.PruneRunsByPipeline(ctx, pipeline.ID, keepBuilds, cutoff); err != nil {
		logger.Warn("pipeline.retention.prune_failed", "error", err)
	}
}

// minRetentionLimits returns the most restrictive (smallest non-zero) builds
// and days retention values across all jobs.
func minRetentionLimits(jobs backwards.Jobs) (int, int) {
	keepBuilds, keepDays := 0, 0

	for _, job := range jobs {
		if job.BuildLogRetention == nil {
			continue
		}

		if job.BuildLogRetention.Builds > 0 {
			if keepBuilds == 0 || job.BuildLogRetention.Builds < keepBuilds {
				keepBuilds = job.BuildLogRetention.Builds
			}
		}

		if job.BuildLogRetention.Days > 0 {
			if keepDays == 0 || job.BuildLogRetention.Days < keepDays {
				keepDays = job.BuildLogRetention.Days
			}
		}
	}

	return keepBuilds, keepDays
}

// resolveDriverFactory returns a driver factory for the given pipeline.
// It reads the pipeline's driver name and any driver config from secrets.
// Fallback: if the pipeline has no pipeline-specific config, the server's
// default config is used.
func (s *ExecutionService) resolveDriverFactory(ctx context.Context, pipeline *storage.Pipeline, logger *slog.Logger) func(ctx context.Context, namespace string) (orchestra.Driver, error) {
	driverName := pipeline.Driver
	if driverName == "" {
		driverName = s.DefaultDriver
	}

	// Attempt to load pipeline-specific driver config from secrets.
	var serverCfg orchestra.DriverConfig
	if s.SecretsManager != nil {
		scope := secrets.PipelineScope(pipeline.ID)

		raw, err := s.SecretsManager.Get(ctx, scope, "driver_config")
		if err == nil && raw != "" {
			cfg, unmarshalErr := unmarshalDriverConfig(driverName, json.RawMessage(raw))
			if unmarshalErr == nil {
				serverCfg = cfg
			}
		}
	}

	// Fall back to server-level config for this driver.
	if serverCfg == nil {
		if cfg, ok := s.DriverConfigs[driverName]; ok {
			serverCfg = cfg
		}
	}

	logger.Info("driver.resolve", "driver", driverName)

	return func(ctx context.Context, ns string) (orchestra.Driver, error) {
		d, err := createDriver(ctx, driverName, ns, serverCfg, logger)
		if err != nil {
			return nil, err
		}

		if s.CacheStore != nil {
			return cache.WrapWithCaching(d, s.CacheStore, s.CacheCompression, s.CacheKeyPrefix, logger), nil
		}

		return d, nil
	}
}

// resolveExecutableContent returns JS/TS content ready for the runtime.
// When the pipeline was stored as YAML it is transpiled on the fly so that
// the latest pipeline_runner.ts bundle is always used. JS and TS content is
// returned as-is.
func resolveExecutableContent(pipeline *storage.Pipeline) (string, error) {
	if pipeline.ContentType == storage.ContentTypeYAML {
		ts, err := backwards.NewPipelineFromContent(pipeline.Content)
		if err != nil {
			return "", fmt.Errorf("could not transpile YAML pipeline %q: %w", pipeline.Name, err)
		}

		return ts, nil
	}

	return pipeline.Content, nil
}
