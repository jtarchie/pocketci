package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/cache"
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
	// Driver, if set, is used for pipeline execution instead of creating
	// one from the driver DSN. The caller owns the driver lifecycle.
	Driver orchestra.Driver
}

// ExecutePipeline executes a pipeline with the given content and driver DSN.
// It handles driver initialization, execution, and cleanup.
func ExecutePipeline(
	ctx context.Context,
	content string,
	driverDSN string,
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

	logger = logger.WithGroup("executor").With(
		"namespace", namespace,
		"driver", sanitizeDriverName(driverDSN),
	)
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
	} else {
		driverConfig, orchestrator, err := orchestra.GetFromDSN(driverDSN)
		if err != nil {
			return fmt.Errorf("could not parse driver DSN: %w", err)
		}

		// Use namespace from DSN if provided
		if driverConfig.Namespace != "" {
			namespace = driverConfig.Namespace
		}

		driver, err = orchestrator(namespace, logger, driverConfig.Params)
		if err != nil {
			return fmt.Errorf("could not create orchestrator client: %w", err)
		}
		defer func() { _ = driver.Close() }()

		// Wrap driver with caching if cache parameters are present
		driver, err = cache.WrapWithCaching(driver, driverConfig.Params, logger)
		if err != nil {
			return fmt.Errorf("could not initialize cache layer: %w", err)
		}
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

func sanitizeDriverName(driverDSN string) string {
	config, err := orchestra.ParseDriverDSN(driverDSN)
	if err != nil || config == nil || config.Name == "" {
		return "unknown"
	}

	return config.Name
}
