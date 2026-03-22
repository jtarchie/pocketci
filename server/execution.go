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
	"github.com/jtarchie/pocketci/storage"
)

// ErrRunNotInFlight is returned when a stop is requested for a run that is not currently executing.
var ErrRunNotInFlight = errors.New("run is not currently in flight")

// ExecutionService manages pipeline execution with concurrency limits.
type ExecutionService struct {
	store                 storage.Driver
	logger                *slog.Logger
	maxInFlight           int
	inFlight              atomic.Int32
	mu                    sync.Mutex
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
	stopRegistry          map[string]context.CancelFunc
	stopMu                sync.Mutex
}

// NewExecutionService creates a new execution service.
// The allowedDrivers list determines the default driver (first in list).
// If allowedDrivers is empty or contains "*", defaults to "docker".
func NewExecutionService(store storage.Driver, logger *slog.Logger, maxInFlight int, allowedDrivers []string) *ExecutionService {
	if maxInFlight <= 0 {
		maxInFlight = 10 // default limit
	}

	// Determine default driver: first allowed driver, or "docker" if wildcard/empty
	defaultDriver := "docker"
	if len(allowedDrivers) > 0 && allowedDrivers[0] != "*" {
		defaultDriver = allowedDrivers[0]
	}

	return &ExecutionService{
		store:         store,
		logger:        logger.WithGroup("executor.run"),
		maxInFlight:   maxInFlight,
		DefaultDriver: defaultDriver,
		stopRegistry:  make(map[string]context.CancelFunc),
	}
}

// Wait blocks until all in-flight pipeline executions have completed.
// This is useful for graceful shutdown or testing.
func (s *ExecutionService) Wait() {
	s.wg.Wait()
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
func (s *ExecutionService) TriggerPipeline(ctx context.Context, pipeline *storage.Pipeline, args []string) (*storage.PipelineRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	requestID, _ := RequestIDFromContext(ctx)
	actor, _ := RequestActorFromContext(ctx)

	// Create run record with queued status
	run, err := s.store.SaveRun(ctx, pipeline.ID)
	if err != nil {
		return nil, err
	}

	// Increment in-flight counter and WaitGroup
	s.inFlight.Add(1)
	s.wg.Add(1)

	// Launch execution goroutine
	go s.executePipeline(pipeline, run, execOptions{args: args, requestID: requestID, authProvider: actor.Provider, user: actor.User})

	return run, nil
}

// TriggerWebhookPipeline starts a new pipeline execution triggered by a webhook.
// It passes webhook request data and a response channel through to the pipeline runtime.
func (s *ExecutionService) TriggerWebhookPipeline(
	ctx context.Context,
	pipeline *storage.Pipeline,
	webhookData *jsapi.WebhookData,
	responseChan chan *jsapi.HTTPResponse,
) (*storage.PipelineRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	requestID, _ := RequestIDFromContext(ctx)
	actor, _ := RequestActorFromContext(ctx)

	// Create run record with queued status
	run, err := s.store.SaveRun(ctx, pipeline.ID)
	if err != nil {
		return nil, err
	}

	// Increment in-flight counter and WaitGroup
	s.inFlight.Add(1)
	s.wg.Add(1)

	// Launch execution goroutine with webhook data
	go s.executePipeline(pipeline, run, execOptions{
		webhook: &webhookExecData{
			webhookData:  webhookData,
			responseChan: responseChan,
		},
		requestID:    requestID,
		authProvider: actor.Provider,
		user:         actor.User,
	})

	return run, nil
}

// ResumePipeline resumes a failed or aborted pipeline run.
// It reuses the existing run ID so that the ResumableRunner can load
// previous state and skip completed steps.
func (s *ExecutionService) ResumePipeline(ctx context.Context, pipeline *storage.Pipeline, run *storage.PipelineRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	requestID, _ := RequestIDFromContext(ctx)
	actor, _ := RequestActorFromContext(ctx)

	// Reset run status to queued for the resumed execution
	if err := s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusQueued, ""); err != nil {
		return fmt.Errorf("failed to reset run status: %w", err)
	}

	s.inFlight.Add(1)
	s.wg.Add(1)

	go s.executePipeline(pipeline, run, execOptions{resume: true, requestID: requestID, authProvider: actor.Provider, user: actor.User})

	return nil
}

// RecoverOrphanedRuns handles runs that were in-flight when the server stopped.
// If the resume feature is enabled, resume-enabled pipelines are restarted;
// otherwise all orphaned runs are marked as failed.
func (s *ExecutionService) RecoverOrphanedRuns(ctx context.Context) {
	runs, err := s.store.GetRunsByStatus(ctx, storage.RunStatusRunning)
	if err != nil {
		s.logger.Error("orphan.recovery.list_failed", "error", err)
		return
	}

	resumeEnabled := IsFeatureEnabled(FeatureResume, s.AllowedFeatures)

	for _, run := range runs {
		logger := s.logger.With("run_id", run.ID, "pipeline_id", run.PipelineID)

		if resumeEnabled {
			pipeline, pErr := s.store.GetPipeline(ctx, run.PipelineID)
			if pErr != nil {
				logger.Error("orphan.recovery.get_pipeline_failed", "error", pErr)
				_ = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "Server restarted; pipeline not found for resume")
				continue
			}

			if pipeline.ResumeEnabled {
				logger.Info("orphan.recovery.resuming")
				runCopy := run
				if rErr := s.ResumePipeline(ctx, pipeline, &runCopy); rErr != nil {
					logger.Error("orphan.recovery.resume_failed", "error", rErr)
					_ = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "Server restarted; resume failed: "+rErr.Error())
				}
				continue
			}
		}

		logger.Info("orphan.recovery.marking_failed")
		_ = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "Server restarted during execution")
		_ = s.store.UpdateStatusForPrefix(ctx, "/pipeline/"+run.ID+"/", []string{"pending", "running"}, "aborted")
	}
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
	resume       bool
	requestID    string
	authProvider string
	user         string
}

func (s *ExecutionService) executePipeline(pipeline *storage.Pipeline, run *storage.PipelineRun, opts execOptions) {
	defer s.inFlight.Add(-1)
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

	// dbCtx is a separate context for storage operations that must succeed
	// even when the pipeline context has been cancelled (e.g. user-initiated stop).
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

	// Update status to running
	err := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusRunning, "")
	if err != nil {
		logger.Error("run.update.failed.to_running", "error", err)
		return
	}

	logger.Info("pipeline.execute.start")

	// Execute the pipeline
	execOpts := runtime.ExecutorOptions{
		RunID:        run.ID,
		PipelineID:   pipeline.ID,
		Resume:       IsFeatureEnabled(FeatureResume, s.AllowedFeatures) && (opts.resume || pipeline.ResumeEnabled),
		RequestID:    opts.requestID,
		AuthProvider: opts.authProvider,
		User:         opts.user,
		Args:         opts.args,
	}

	// Only pass secrets manager if the secrets feature is enabled
	if IsFeatureEnabled(FeatureSecrets, s.AllowedFeatures) {
		execOpts.SecretsManager = s.SecretsManager
	}

	// Only pass webhook data if the webhooks feature is enabled
	if opts.webhook != nil && IsFeatureEnabled(FeatureWebhooks, s.AllowedFeatures) {
		execOpts.WebhookData = opts.webhook.webhookData
		execOpts.ResponseChan = opts.webhook.responseChan
	}

	// Disable notifications if the feature is not enabled
	execOpts.DisableNotifications = !IsFeatureEnabled(FeatureNotifications, s.AllowedFeatures)

	// Disable fetch if the feature is not enabled
	execOpts.DisableFetch = !IsFeatureEnabled(FeatureFetch, s.AllowedFeatures)
	execOpts.FetchTimeout = s.FetchTimeout
	execOpts.FetchMaxResponseBytes = s.FetchMaxResponseBytes

	executableContent, err := resolveExecutableContent(pipeline)
	if err != nil {
		logger.Error("pipeline.transpile.failed", "error", err)

		updateErr := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusFailed, err.Error())
		if updateErr != nil {
			logger.Error("run.update.failed.to_failed", "error", updateErr)
		}

		return
	}

	execOpts.DriverFactory = s.resolveDriverFactory(pipeline, logger)
	err = runtime.ExecutePipeline(ctx, executableContent, s.store, logger, execOpts)

	// If the context was cancelled (user stop or otherwise), mark as failed
	// regardless of what ExecutePipeline returned.
	if ctx.Err() != nil {
		_ = s.store.UpdateStatusForPrefix(dbCtx, "/pipeline/"+run.ID+"/", []string{"pending", "running"}, "aborted")

		updateErr := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusFailed, "Run stopped by user")
		if updateErr != nil {
			logger.Error("run.update.failed.to_failed", "error", updateErr)
		}

		return
	}

	if err != nil {
		logger.Error("pipeline.execute.failed", "error", err)

		updateErr := s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusFailed, err.Error())
		if updateErr != nil {
			logger.Error("run.update.failed.to_failed", "error", updateErr)
		}

		return
	}

	// Check if any jobs failed by querying job statuses
	finalStatus, errMsg := s.determineRunStatus(dbCtx, run.ID, logger)

	err = s.store.UpdateRunStatus(dbCtx, run.ID, finalStatus, errMsg)
	if err != nil {
		logger.Error("run.update.failed.to_final", "error", err)
		return
	}

	// Post-commit re-check: if a stop arrived while we were finalizing,
	// overwrite whatever we just committed with "failed". StopRun also
	// writes "failed" directly as a safety net, so between the two any
	// ordering of goroutine-vs-StopRun results in "failed".
	if ctx.Err() != nil {
		_ = s.store.UpdateStatusForPrefix(dbCtx, "/pipeline/"+run.ID+"/", []string{"pending", "running"}, "aborted")
		_ = s.store.UpdateRunStatus(dbCtx, run.ID, storage.RunStatusFailed, "Run stopped by user")

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

	run, err := s.store.SaveRun(ctx, pipeline.ID)
	if err != nil {
		return fmt.Errorf("failed to save run: %w", err)
	}

	if err = s.store.UpdateRunStatus(ctx, run.ID, storage.RunStatusRunning, ""); err != nil {
		s.logger.Error("run.update.failed.to_running", "error", err)
	}

	// --- Pre-seed workdir volume (consumes HTTP body before SSE starts) ---
	var preseededVolumes map[string]orchestra.Volume
	var driver orchestra.Driver

	factory := s.resolveDriverFactory(pipeline, s.logger)

	if workdirTar != nil {
		namespace := "ci-" + run.ID

		var dErr error

		driver, dErr = factory(namespace)
		if dErr != nil {
			return fmt.Errorf("could not create driver: %w", dErr)
		}

		vol, vErr := driver.CreateVolume(ctx, "workdir", 0)
		if vErr != nil {
			_ = driver.Close()
			return fmt.Errorf("could not create workdir volume: %w", vErr)
		}

		accessor, ok := driver.(cache.VolumeDataAccessor)
		if !ok {
			_ = vol.Cleanup(ctx)
			_ = driver.Close()
			return fmt.Errorf("driver %q does not support volume data access", driver.Name())
		}

		s.logger.Info("workdir.preseed.start")

		if cErr := accessor.CopyToVolume(ctx, vol.Name(), workdirTar); cErr != nil {
			_ = vol.Cleanup(ctx)
			_ = driver.Close()
			return fmt.Errorf("could not seed workdir volume: %w", cErr)
		}

		s.logger.Info("workdir.preseed.done")

		preseededVolumes = map[string]orchestra.Volume{"workdir": vol}
		// Close the driver after RunByNameSync returns (after ExecutePipeline
		// completes). ExecutePipeline reuses this driver instance via opts.Driver
		// and skips creating/closing its own.
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

	exitCode := 0
	var finalStatus storage.RunStatus
	errMsg := ""

	if execErr != nil {
		exitCode = 1
		finalStatus = storage.RunStatusFailed
		// TODO: we never display this error message anywhere in the UI - consider surfacing it in the run details page or similar
		errMsg = execErr.Error()
	} else {
		var jobErrMsg string
		finalStatus, jobErrMsg = s.determineRunStatus(ctx, run.ID, s.logger)
		if finalStatus == storage.RunStatusFailed {
			exitCode = 1
			errMsg = jobErrMsg
		}
	}

	if err = s.store.UpdateRunStatus(ctx, run.ID, finalStatus, errMsg); err != nil {
		s.logger.Error("run.update.failed.to_final", "error", err)
	}

	s.pruneOldRuns(ctx, pipeline, s.logger)

	// Write SSE exit event.
	exitEvent := map[string]any{"event": "exit", "code": exitCode, "run_id": run.ID}
	if errMsg != "" {
		exitEvent["message"] = errMsg
	}

	exitData, _ := json.Marshal(exitEvent)
	fmt.Fprintf(w, "data: %s\n\n", exitData) //nolint:errcheck
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

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

	keepBuilds, keepDays := 0, 0

	for _, job := range cfg.Jobs {
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

// resolveDriverFactory returns a driver factory for the given pipeline.
// It reads the pipeline's driver name and any driver config from secrets.
// Fallback: if the pipeline has no pipeline-specific config, the server's
// default config is used.
func (s *ExecutionService) resolveDriverFactory(pipeline *storage.Pipeline, logger *slog.Logger) func(namespace string) (orchestra.Driver, error) {
	driverName := pipeline.Driver
	if driverName == "" {
		driverName = s.DefaultDriver
	}

	// Attempt to load pipeline-specific driver config from secrets.
	var serverCfg orchestra.DriverConfig
	if s.SecretsManager != nil {
		scope := secrets.PipelineScope(pipeline.ID)

		raw, err := s.SecretsManager.Get(context.Background(), scope, "driver_config")
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

	return func(ns string) (orchestra.Driver, error) {
		d, err := createDriver(driverName, ns, serverCfg, logger)
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
