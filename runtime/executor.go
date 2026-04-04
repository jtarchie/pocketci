package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	backwards "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	runtimebackwards "github.com/jtarchie/pocketci/runtime/backwards"
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
	// ContentType indicates the pipeline content format. When set to
	// ContentTypeYAML the Go-native runner is used instead of the JS VM.
	ContentType storage.ContentType
}

// initExecutionDriver creates or reuses a driver for pipeline execution.
// Returns the driver, a cleanup function, the updated logger, and any error.
func initExecutionDriver(ctx context.Context, opts ExecutorOptions, namespace string, logger *slog.Logger) (orchestra.Driver, func(), *slog.Logger, error) {
	if opts.Driver != nil {
		return opts.Driver, func() {}, logger.With("driver", opts.Driver.Name()), nil
	}

	if opts.DriverFactory == nil {
		return nil, nil, logger, errors.New("no driver factory configured")
	}

	driver, err := opts.DriverFactory(ctx, namespace)
	if err != nil {
		return nil, nil, logger, fmt.Errorf("could not create orchestrator: %w", err)
	}

	cleanup := func() { _ = driver.Close() }

	return driver, cleanup, logger.With("driver", driver.Name()), nil
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

	driver, cleanup, logger, err := initExecutionDriver(ctx, opts, namespace, logger)
	if err != nil {
		return err
	}

	defer cleanup()

	logger.Info("pipeline.executing")

	if opts.ContentType == storage.ContentTypeYAML {
		cfg, err := backwards.ParseConfig(content)
		if err != nil {
			return fmt.Errorf("could not parse YAML pipeline: %w", err)
		}

		runID := opts.RunID
		if runID == "" {
			runID = namespace
		}

		runner := runtimebackwards.New(cfg, driver, store, logger, runID, opts.PipelineID,
			runtimebackwards.RunnerOptions{
				SecretsManager: opts.SecretsManager,
				WebhookData:    opts.WebhookData,
				TargetJobs:     opts.TargetJobs,
				DedupTTL:       opts.DedupTTL,
			},
		)

		if err := runner.Run(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return fmt.Errorf("execution cancelled: %w", err)
			}

			return fmt.Errorf("could not execute pipeline: %w", err)
		}

		logger.Info("pipeline.completed.success")

		return nil
	}

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
