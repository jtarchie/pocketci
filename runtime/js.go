package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/console"
	"github.com/dop251/goja_nodejs/require"
	"github.com/evanw/esbuild/pkg/api"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
	"github.com/jtarchie/pocketci/webhooks/filter"
)

// ExecuteOptions configures pipeline execution.
type ExecuteOptions struct {
	// Resume enables resume mode for the pipeline.
	Resume bool
	// RunID is the unique identifier for this pipeline run.
	// If resuming, this should match the previous run's ID.
	RunID string
	// PipelineID is the unique identifier for this pipeline.
	// Used to scope resource versions to a specific pipeline.
	PipelineID string
	// Namespace is the namespace for this execution.
	Namespace string
	// WebhookData contains the incoming HTTP request when triggered via webhook.
	WebhookData *jsapi.WebhookData
	// ResponseChan receives the HTTP response from the pipeline.
	ResponseChan chan *jsapi.HTTPResponse
	// SecretsManager provides access to encrypted secrets for this pipeline.
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
	// When the pipeline calls runtime.createVolume(name), a matching pre-created
	// volume is reused instead of allocating a new one.
	PreseededVolumes map[string]orchestra.Volume
	// OutputCallback, if set, is applied to every container task so that
	// stdout/stderr chunks are forwarded to the caller in real time.
	OutputCallback runner.OutputCallback
}

type JS struct {
	logger *slog.Logger
}

func NewJS(logger *slog.Logger) *JS {
	return &JS{
		logger: logger.WithGroup("js"),
	}
}

// TranspileAndValidate transpiles TypeScript/JavaScript source code to executable JavaScript.
// It performs esbuild transpilation, wraps the code for module exports, and validates
// the result can be compiled by goja. Returns the ready-to-execute code or an error.
func TranspileAndValidate(source string) (string, error) {
	result := api.Transform(source, api.TransformOptions{
		Loader:     api.LoaderTS,
		Format:     api.FormatCommonJS,
		Target:     api.ES2017,
		Sourcemap:  api.SourceMapInline,
		Platform:   api.PlatformNeutral,
		Sourcefile: "main.js",
	})

	if len(result.Errors) > 0 {
		return "", fmt.Errorf("syntax error: %s", result.Errors[0].Text)
	}

	lines := strings.Split(strings.TrimSpace(string(result.Code)), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("empty pipeline after transpilation: %w", errors.ErrUnsupported)
	}

	var sourceMap string
	sourceMap, lines = lines[len(lines)-1], lines[:len(lines)-1]
	finalSource := "{(function() { const module = {}; " + strings.Join(lines, "\n") +
		"; return module.exports.pipeline;}).apply(undefined)}\n" +
		sourceMap

	_, err := goja.Compile("main.js", finalSource, true)
	if err != nil {
		return "", fmt.Errorf("compilation error: %w", err)
	}

	return finalSource, nil
}

// Execute runs a pipeline with default options (no resume).
func (j *JS) Execute(ctx context.Context, source string, driver orchestra.Driver, storage storage.Driver) error {
	return j.ExecuteWithOptions(ctx, source, driver, storage, ExecuteOptions{})
}

// ExecuteWithOptions runs a pipeline with the given options.
func (j *JS) ExecuteWithOptions(ctx context.Context, source string, driver orchestra.Driver, storage storage.Driver, opts ExecuteOptions) error {
	var r runner.Runner

	if opts.Resume {
		resumableRunner, err := runner.NewResumableRunner(ctx, driver, storage, j.logger, opts.Namespace, runner.ResumeOptions{
			RunID:  opts.RunID,
			Resume: opts.Resume,
		})
		if err != nil {
			return fmt.Errorf("could not create resumable runner: %w", err)
		}

		if opts.SecretsManager != nil {
			resumableRunner.SetSecretsManager(opts.SecretsManager, opts.PipelineID)
		}

		if opts.PreseededVolumes != nil {
			resumableRunner.SetPreseededVolumes(opts.PreseededVolumes)
		}

		if opts.OutputCallback != nil {
			resumableRunner.SetOutputCallback(opts.OutputCallback)
		}

		r = resumableRunner
	} else {
		pipelineRunner := runner.NewPipelineRunner(ctx, driver, storage, j.logger, opts.Namespace, opts.RunID)
		if opts.SecretsManager != nil {
			pipelineRunner.SetSecretsManager(opts.SecretsManager, opts.PipelineID)
		}

		if opts.PreseededVolumes != nil {
			pipelineRunner.SetPreseededVolumes(opts.PreseededVolumes)
		}

		if opts.OutputCallback != nil {
			pipelineRunner.SetOutputCallback(opts.OutputCallback)
		}

		r = pipelineRunner
	}

	finalSource, err := TranspileAndValidate(source)
	if err != nil {
		return err
	}

	program, err := goja.Compile(
		"main.js",
		finalSource,
		true,
	)
	if err != nil {
		return fmt.Errorf("could not compile: %w", err)
	}

	// this is setup to build the pipeline in a goja jsVM
	jsVM := goja.New()
	jsVM.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

	if timeout, ok := ctx.Deadline(); ok {
		// https://github.com/dop251/goja?tab=readme-ov-file#interrupting
		time.AfterFunc(time.Until(timeout), func() {
			jsVM.Interrupt("context deadline exceeded")
		})
	}

	registry := require.NewRegistry()
	registry.Enable(jsVM)
	registry.RegisterNativeModule("console", console.RequireWithPrinter(jsapi.NewPrinter(
		j.logger.WithGroup("console.log"),
	)))

	_ = jsVM.Set("console", require.Require(jsVM, "console"))

	err = jsVM.Set("assert", jsapi.NewAssert(jsVM, j.logger))
	if err != nil {
		return fmt.Errorf("could not set assert: %w", err)
	}

	err = jsVM.Set("YAML", jsapi.NewYAML(jsVM, j.logger))
	if err != nil {
		return fmt.Errorf("could not set YAML: %w", err)
	}

	runtime := NewRuntime(jsVM, r, opts.Namespace, opts.RunID)
	runtime.secretsManager = opts.SecretsManager
	runtime.pipelineID = opts.PipelineID
	runtime.ctx = ctx
	runtime.storage = storage

	err = jsVM.Set("runtime", runtime)
	if err != nil {
		return fmt.Errorf("could not set runtime: %w", err)
	}

	// Set up notification runtime (disabled when feature is gated)
	notifier := jsapi.NewNotifier(j.logger)
	notifier.Disabled = opts.DisableNotifications
	if opts.SecretsManager != nil {
		notifier.SetSecretsManager(opts.SecretsManager, opts.PipelineID)
	}
	notifyRuntime := jsapi.NewNotifyRuntime(ctx, jsVM, notifier, runtime.promises, runtime.tasks)

	err = jsVM.Set("notify", notifyRuntime)
	if err != nil {
		return fmt.Errorf("could not set notify: %w", err)
	}

	// Set up native resource runner
	resourceRunner := runner.NewResourceRunner(ctx, j.logger)
	if opts.SecretsManager != nil {
		resourceRunner.SetSecretsManager(opts.SecretsManager, opts.PipelineID)
	}

	err = jsVM.Set("nativeResources", resourceRunner)
	if err != nil {
		return fmt.Errorf("could not set nativeResources: %w", err)
	}

	// Wrap storage to inject context automatically for JavaScript calls
	storageWrapper := &storageContextWrapper{
		driver: storage,
		ctx:    ctx,
	}
	err = jsVM.Set("storage", storageWrapper)
	if err != nil {
		return fmt.Errorf("could not set storage: %w", err)
	}

	// Set up fetch runtime for outbound HTTP requests
	fetchRuntime := jsapi.NewFetchRuntime(ctx, jsVM, runtime.promises, runtime.tasks, opts.FetchTimeout, opts.FetchMaxResponseBytes)
	fetchRuntime.Disabled = opts.DisableFetch

	err = jsVM.Set("fetch", fetchRuntime.Fetch)
	if err != nil {
		return fmt.Errorf("could not set fetch: %w", err)
	}

	// Set up HTTP runtime for webhook support
	httpRuntime := jsapi.NewHTTPRuntime(jsVM, opts.WebhookData, opts.ResponseChan)

	err = jsVM.Set("http", httpRuntime)
	if err != nil {
		return fmt.Errorf("could not set http: %w", err)
	}

	// webhookTrigger evaluates an expr-lang expression against the current webhook data.
	// Returns true when no webhook is active (manual trigger) so jobs always run.
	err = jsVM.Set("webhookTrigger", func(expression string) bool {
		if opts.WebhookData == nil {
			return true
		}

		env := filter.WebhookEnv{
			Provider:  opts.WebhookData.Provider,
			EventType: opts.WebhookData.EventType,
			Method:    opts.WebhookData.Method,
			Headers:   opts.WebhookData.Headers,
			Query:     opts.WebhookData.Query,
			Body:      opts.WebhookData.Body,
		}

		var payload map[string]any
		if jsonErr := json.Unmarshal([]byte(opts.WebhookData.Body), &payload); jsonErr == nil {
			env.Payload = payload
		}

		result, evalErr := filter.Evaluate(expression, env)
		if evalErr != nil {
			slog.Error("webhookTrigger evaluation failed", "error", evalErr, "expression", expression)

			return false
		}

		return result
	})
	if err != nil {
		return fmt.Errorf("could not set webhookTrigger: %w", err)
	}

	// webhookParams evaluates a map of expr-lang string expressions against the current
	// webhook data, returning a map of resolved string values. Returns an empty map when
	// no webhook is active so callers can always treat the result as a plain string map.
	err = jsVM.Set("webhookParams", func(paramsExprs map[string]string) map[string]string {
		result := make(map[string]string, len(paramsExprs))

		if opts.WebhookData == nil {
			return result
		}

		env := filter.WebhookEnv{
			Provider:  opts.WebhookData.Provider,
			EventType: opts.WebhookData.EventType,
			Method:    opts.WebhookData.Method,
			Headers:   opts.WebhookData.Headers,
			Query:     opts.WebhookData.Query,
			Body:      opts.WebhookData.Body,
		}

		var payload map[string]any
		if jsonErr := json.Unmarshal([]byte(opts.WebhookData.Body), &payload); jsonErr == nil {
			env.Payload = payload
		}

		for key, expression := range paramsExprs {
			val, evalErr := filter.EvaluateString(expression, env)
			if evalErr != nil {
				slog.Error("webhookParams evaluation failed", "key", key, "error", evalErr, "expression", expression)

				continue
			}

			result[key] = val
		}

		return result
	})
	if err != nil {
		return fmt.Errorf("could not set webhookParams: %w", err)
	}

	// Expose pipeline context to JavaScript (runID, pipelineID, etc.)
	triggeredBy := "manual"
	if opts.WebhookData != nil {
		triggeredBy = "webhook"
	}
	runtime.triggeredBy = triggeredBy

	args := opts.Args
	if args == nil {
		args = []string{}
	}

	pipelineContext := map[string]any{
		"runID":       opts.RunID,
		"pipelineID":  opts.PipelineID,
		"triggeredBy": triggeredBy,
		"args":        args,
	}
	if driver != nil {
		pipelineContext["driverName"] = driver.Name()
	}
	err = jsVM.Set("pipelineContext", pipelineContext)
	if err != nil {
		return fmt.Errorf("could not set pipelineContext: %w", err)
	}

	pipeline, err := jsVM.RunProgram(program)
	if err != nil {
		defer jsVM.ClearInterrupt()

		return fmt.Errorf("could not run program: %w", err)
	}

	// let's run the pipeline
	pipelineFunc, found := goja.AssertFunction(pipeline)
	if !found {
		return ErrPipelineNotFunction
	}

	value, err := pipelineFunc(goja.Undefined())
	if err != nil {
		return fmt.Errorf("could not run pipeline: %w", err)
	}

	if value == nil {
		return fmt.Errorf("pipeline returned nil: %w", ErrPipelineReturnedNonPromise)
	}

	promise, found := value.Export().(*goja.Promise)
	if !found {
		return fmt.Errorf("pipeline did not return a promise: %w", ErrPipelineNotFunction)
	}

	err = runtime.Wait()
	if err != nil {
		// Mark in-progress steps as aborted if using resumable runner
		if resumable, ok := r.(*runner.ResumableRunner); ok {
			resumable.MarkInProgressAsAborted()
		}

		return fmt.Errorf("pipeline did not successfully execute: %w", err)
	}

	// If the context was cancelled, mark any remaining in-progress steps as aborted
	if ctx.Err() != nil {
		if resumable, ok := r.(*runner.ResumableRunner); ok {
			resumable.MarkInProgressAsAborted()
		}
	}

	// Cleanup volumes after pipeline completes - this triggers cache persistence
	err = r.CleanupVolumes()
	if err != nil {
		j.logger.Error("volume.cleanup.failed", "err", err)
		// Don't fail the pipeline on volume cleanup errors, just log
	}

	if promise.State() == goja.PromiseStateRejected {
		res := promise.Result()
		if resObj, ok := res.(*goja.Object); ok {
			if stack := resObj.Get("stack"); stack != nil {
				return fmt.Errorf("%w: %v\n%v", ErrPromiseRejected, res, stack)
			}
		}

		return fmt.Errorf("%w: %v", ErrPromiseRejected, res)
	}

	return nil
}

var (
	ErrPipelineNotFunction        = errors.New("pipeline is not a function")
	ErrPipelineReturnedNonPromise = errors.New("pipeline did not return a promise")
	ErrPromiseRejected            = errors.New("promise rejected")
)

// storageContextWrapper wraps a storage.Driver to automatically inject context
// for JavaScript calls that don't pass context explicitly.
type storageContextWrapper struct {
	driver storage.Driver
	ctx    context.Context
}

// Set wraps the storage Set method, injecting context automatically.
func (w *storageContextWrapper) Set(prefix string, payload any) error {
	return w.driver.Set(w.ctx, prefix, payload)
}

// Get wraps the storage Get method, injecting context automatically.
func (w *storageContextWrapper) Get(prefix string) (storage.Payload, error) {
	return w.driver.Get(w.ctx, prefix)
}

// GetAll wraps the storage GetAll method, injecting context automatically.
func (w *storageContextWrapper) GetAll(prefix string, fields []string) (storage.Results, error) {
	return w.driver.GetAll(w.ctx, prefix, fields)
}

// SavePipeline wraps the storage SavePipeline method, injecting context automatically.
// Pipelines saved from within a running pipeline are always JS/TS content.
func (w *storageContextWrapper) SavePipeline(name, content, driver, _ string) (*storage.Pipeline, error) {
	return w.driver.SavePipeline(w.ctx, name, content, driver, "js")
}

// GetPipeline wraps the storage GetPipeline method, injecting context automatically.
func (w *storageContextWrapper) GetPipeline(id string) (*storage.Pipeline, error) {
	return w.driver.GetPipeline(w.ctx, id)
}

// ListPipelines wraps the storage SearchPipelines method with empty query and default
// pagination, injecting context automatically.
func (w *storageContextWrapper) ListPipelines() (*storage.PaginationResult[storage.Pipeline], error) {
	return w.driver.SearchPipelines(w.ctx, "", 1, 20)
}

// DeletePipeline wraps the storage DeletePipeline method, injecting context automatically.
func (w *storageContextWrapper) DeletePipeline(id string) error {
	return w.driver.DeletePipeline(w.ctx, id)
}

// Close wraps the storage Close method (no context needed).
func (w *storageContextWrapper) Close() error {
	return w.driver.Close()
}
