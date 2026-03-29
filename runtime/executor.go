package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

// ExecutorOptions configures pipeline execution.
type ExecutorOptions struct {
	// Resume enables resume mode for the pipeline.
	Resume bool
	// RunID is the unique identifier for this pipeline run.
	// If resuming, this should match the previous run's ID.
	RunID string
	// PipelineID is the unique identifier for this pipeline.
	// Used to scope resource versions to a specific pipeline.
	PipelineID string
	// RequestID is the inbound HTTP request ID that triggered this run.
	RequestID string
	// AuthProvider identifies the auth mechanism/provider of the caller.
	AuthProvider string
	// User identifies the authenticated caller.
	User string
	// Namespace is the namespace for this execution (internal use).
	Namespace string
	// WebhookData contains the incoming HTTP request when triggered via webhook.
	// Nil when not triggered via webhook.
	WebhookData *jsapi.WebhookData
	// ResponseChan receives the HTTP response from the pipeline.
	// Nil when not triggered via webhook.
	ResponseChan chan *jsapi.HTTPResponse
	// SecretsManager provides access to encrypted secrets.
	// If nil, secret resolution is disabled.
	SecretsManager secrets.Manager
	// DisableNotifications prevents the notify system from sending messages.
	DisableNotifications bool
	// DisableFetch prevents the fetch() function from making outbound HTTP requests.
	DisableFetch bool
	// FetchTimeout is the default timeout for fetch() calls.
	FetchTimeout time.Duration
	// FetchMaxResponseBytes is the maximum response body size for fetch() calls.
	FetchMaxResponseBytes int64
	// Args contains CLI arguments passed to the pipeline via pipelineContext.args.
	Args []string
	// PreseededVolumes maps volume names to pre-created, already-seeded volumes.
	// When the pipeline calls runtime.createVolume("name"), a matching
	// pre-created volume is reused instead of creating a new one.
	PreseededVolumes map[string]orchestra.Volume
	// OutputCallback, if set, is applied to every container task so that
	// stdout/stderr chunks are forwarded to the caller in real time.
	OutputCallback func(stream string, data string)
	// DriverFactory, if set, is called to create a new driver for this execution.
	// Required if Driver is not set.
	DriverFactory func(ctx context.Context, namespace string) (orchestra.Driver, error)
	// Driver, if set, is used for pipeline execution instead of creating
	// one from the DriverFactory. The caller owns the driver lifecycle.
	Driver orchestra.Driver
	// DedupTTL is the time-to-live for webhook dedup entries.
	// If zero, defaults to 7 days.
	DedupTTL time.Duration
	// TargetJobs, when set, limits execution to these specific jobs (and their
	// downstream dependents via passed constraints). Empty means run all jobs.
	TargetJobs []string
	// TriggerCallback, if set, allows pipeline code to trigger other pipelines
	// via the triggerPipeline() JS API.
	TriggerCallback func(ctx context.Context, pipelineName string, jobs []string, args []string) (string, error)
}

// ExecutePipeline executes a pipeline with the given content and driver factory.
// It handles driver initialization, execution, and cleanup.
func ExecutePipeline(
	ctx context.Context,
	content string,
	store storage.Driver,
	logger *slog.Logger,
	opts ExecutorOptions,
) error {
	if logger == nil {
		logger = slog.Default()
	}

	// Generate a namespace for this execution
	namespace := "ci-" + support.UniqueID()
	if opts.RunID != "" {
		namespace = "ci-" + opts.RunID
	}

	logger = logger.WithGroup("executor").With("namespace", namespace)
	if opts.RequestID != "" {
		logger = logger.With("request_id", opts.RequestID)
	}
	if opts.AuthProvider != "" && opts.User != "" {
		logger = logger.With("auth_provider", opts.AuthProvider, "user", opts.User)
	}

	logger.Info("driver.initialize")

	var driver orchestra.Driver

	if opts.Driver != nil {
		// Reuse the caller-provided driver (caller manages lifecycle).
		driver = opts.Driver
		logger = logger.With("driver", driver.Name())
	} else {
		if opts.DriverFactory == nil {
			return errors.New("no driver factory configured")
		}

		var err error

		driver, err = opts.DriverFactory(ctx, namespace)
		if err != nil {
			return fmt.Errorf("could not create orchestrator: %w", err)
		}

		defer func() { _ = driver.Close() }()

		logger = logger.With("driver", driver.Name())
	}

	logger.Info("pipeline.executing")

	js := NewJS(logger)

	executeOpts := ExecuteOptions{
		Resume:                opts.Resume,
		RunID:                 opts.RunID,
		PipelineID:            opts.PipelineID,
		Namespace:             namespace,
		WebhookData:           opts.WebhookData,
		ResponseChan:          opts.ResponseChan,
		SecretsManager:        opts.SecretsManager,
		DisableNotifications:  opts.DisableNotifications,
		DisableFetch:          opts.DisableFetch,
		FetchTimeout:          opts.FetchTimeout,
		FetchMaxResponseBytes: opts.FetchMaxResponseBytes,
		Args:                  opts.Args,
		DedupTTL:              opts.DedupTTL,
		TargetJobs:            opts.TargetJobs,
		TriggerCallback:       opts.TriggerCallback,
	}

	// If pre-seeded volumes were provided, pass them through.
	if opts.PreseededVolumes != nil {
		executeOpts.PreseededVolumes = opts.PreseededVolumes
	}

	if opts.OutputCallback != nil {
		executeOpts.OutputCallback = opts.OutputCallback
	}

	if execErr := js.ExecuteWithOptions(ctx, content, driver, store, executeOpts); execErr != nil {
		return fmt.Errorf("could not execute pipeline: %w", execErr)
	}

	logger.Info("pipeline.completed.success")

	return nil
}
