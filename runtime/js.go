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
	r, err := j.createRunner(ctx, driver, storage, opts)
	if err != nil {
		return err
	}

	finalSource, err := TranspileAndValidate(source)
	if err != nil {
		return err
	}

	program, err := goja.Compile("main.js", finalSource, true)
	if err != nil {
		return fmt.Errorf("could not compile: %w", err)
	}

	jsVM := goja.New()
	jsVM.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

	if timeout, ok := ctx.Deadline(); ok {
		time.AfterFunc(time.Until(timeout), func() {
			jsVM.Interrupt("context deadline exceeded")
		})
	}

	runtime := NewRuntime(jsVM, r, opts.Namespace, opts.RunID)
	runtime.secretsManager = opts.SecretsManager
	runtime.pipelineID = opts.PipelineID
	runtime.ctx = ctx
	runtime.storage = storage

	if err := j.setupJSVM(ctx, jsVM, runtime, driver, storage, opts); err != nil {
		return err
	}

	return j.runPipeline(ctx, jsVM, program, runtime, r)
}

// createRunner initialises the appropriate pipeline runner based on opts.
func (j *JS) createRunner(ctx context.Context, driver orchestra.Driver, storage storage.Driver, opts ExecuteOptions) (runner.Runner, error) {
	var r runner.Runner

	if !opts.Resume {
		r = runner.NewPipelineRunner(ctx, driver, storage, j.logger, opts.Namespace, opts.RunID)
	} else {
		resumableRunner, err := runner.NewResumableRunner(ctx, driver, storage, j.logger, opts.Namespace, runner.ResumeOptions{
			RunID:  opts.RunID,
			Resume: opts.Resume,
		})
		if err != nil {
			return nil, fmt.Errorf("could not create resumable runner: %w", err)
		}

		r = resumableRunner
	}

	if opts.SecretsManager != nil {
		r.SetSecretsManager(opts.SecretsManager, opts.PipelineID)
	}

	if opts.PreseededVolumes != nil {
		r.SetPreseededVolumes(opts.PreseededVolumes)
	}

	if opts.OutputCallback != nil {
		r.SetOutputCallback(opts.OutputCallback)
	}

	return r, nil
}

// setupJSVM registers all global JS bindings (assert, YAML, runtime, notify,
// nativeResources, storage, fetch, http, webhookTrigger, webhookParams,
// pipelineContext) onto the goja VM.
func (j *JS) setupJSVM(
	ctx context.Context,
	jsVM *goja.Runtime,
	runtime *Runtime,
	driver orchestra.Driver,
	storage storage.Driver,
	opts ExecuteOptions,
) error {
	registry := require.NewRegistry()
	registry.Enable(jsVM)
	registry.RegisterNativeModule("console", console.RequireWithPrinter(jsapi.NewPrinter(
		j.logger.WithGroup("console.log"),
	)))

	_ = jsVM.Set("console", require.Require(jsVM, "console"))

	if err := jsVM.Set("assert", jsapi.NewAssert(jsVM, j.logger)); err != nil {
		return fmt.Errorf("could not set assert: %w", err)
	}

	yamlHelper := jsapi.NewYAML(jsVM, j.logger)
	if err := jsVM.Set("yaml", yamlHelper); err != nil {
		return fmt.Errorf("could not set yaml: %w", err)
	}
	// Deprecated alias for backwards compatibility
	if err := jsVM.Set("YAML", yamlHelper); err != nil {
		return fmt.Errorf("could not set YAML: %w", err)
	}

	// Register new focused namespaces
	volumesNS := runtime.Volumes()
	if err := jsVM.Set("volumes", volumesNS); err != nil {
		return fmt.Errorf("could not set volumes: %w", err)
	}

	agentNS := runtime.AgentRT()
	if err := jsVM.Set("agent", agentNS); err != nil {
		return fmt.Errorf("could not set agent: %w", err)
	}

	if err := jsVM.Set("runtime", runtime); err != nil {
		return fmt.Errorf("could not set runtime: %w", err)
	}

	pipelineNS := runtime.PipelineNS()
	if err := jsVM.Set("pipeline", pipelineNS); err != nil {
		return fmt.Errorf("could not set pipeline: %w", err)
	}

	if err := j.setupNotifyAndResources(ctx, jsVM, runtime, opts); err != nil {
		return err
	}

	storageWrapper := &storageContextWrapper{driver: storage, ctx: ctx}
	if err := jsVM.Set("storage", storageWrapper); err != nil {
		return fmt.Errorf("could not set storage: %w", err)
	}

	fetchRuntime := jsapi.NewFetchRuntime(ctx, jsVM, runtime.promises, runtime.tasks, opts.FetchTimeout, opts.FetchMaxResponseBytes)
	fetchRuntime.Disabled = opts.DisableFetch

	if err := jsVM.Set("fetch", fetchRuntime.Fetch); err != nil {
		return fmt.Errorf("could not set fetch: %w", err)
	}

	httpRuntime := jsapi.NewHTTPRuntime(jsVM, opts.WebhookData, opts.ResponseChan)
	if err := jsVM.Set("http", httpRuntime); err != nil {
		return fmt.Errorf("could not set http: %w", err)
	}

	if err := j.setupWebhookBindings(ctx, jsVM, storage, opts); err != nil {
		return err
	}

	if err := j.setupTriggerPipeline(ctx, jsVM, runtime, opts); err != nil {
		return err
	}

	return j.setupPipelineContext(jsVM, runtime, driver, opts)
}

// setupNotifyAndResources registers the notify and nativeResources globals on the VM.
func (j *JS) setupNotifyAndResources(ctx context.Context, jsVM *goja.Runtime, rt *Runtime, opts ExecuteOptions) error {
	notifier := jsapi.NewNotifier(j.logger)
	notifier.Disabled = opts.DisableNotifications

	if opts.SecretsManager != nil {
		notifier.SetSecretsManager(opts.SecretsManager, opts.PipelineID)
	}

	notifyRuntime := jsapi.NewNotifyRuntime(ctx, jsVM, notifier, rt.promises, rt.tasks)

	if err := jsVM.Set("notify", notifyRuntime); err != nil {
		return fmt.Errorf("could not set notify: %w", err)
	}

	resourceRunner := runner.NewResourceRunner(ctx, j.logger)
	if opts.SecretsManager != nil {
		resourceRunner.SetSecretsManager(opts.SecretsManager, opts.PipelineID)
	}

	if err := jsVM.Set("nativeResources", resourceRunner); err != nil {
		return fmt.Errorf("could not set nativeResources: %w", err)
	}

	return nil
}

// defaultDedupTTL is the default time-to-live for webhook dedup entries.
const defaultDedupTTL = 7 * 24 * time.Hour

// setupWebhookBindings registers webhookTrigger, webhookParams, and webhookDedup on the VM.
func (j *JS) setupWebhookBindings(ctx context.Context, jsVM *goja.Runtime, store storage.Driver, opts ExecuteOptions) error {
	if err := jsVM.Set("webhookTrigger", func(expression string) bool {
		if opts.WebhookData == nil {
			return true
		}

		env := buildWebhookEnv(opts.WebhookData)

		result, evalErr := filter.Evaluate(expression, env)
		if evalErr != nil {
			slog.Error("webhookTrigger evaluation failed", "error", evalErr, "expression", expression)

			return false
		}

		return result
	}); err != nil {
		return fmt.Errorf("could not set webhookTrigger: %w", err)
	}

	if err := jsVM.Set("webhookParams", func(paramsExprs map[string]string) map[string]string {
		result := make(map[string]string, len(paramsExprs))

		if opts.WebhookData == nil {
			return result
		}

		env := buildWebhookEnv(opts.WebhookData)

		for key, expression := range paramsExprs {
			val, evalErr := filter.EvaluateString(expression, env)
			if evalErr != nil {
				slog.Error("webhookParams evaluation failed", "key", key, "error", evalErr, "expression", expression)

				continue
			}

			result[key] = val
		}

		return result
	}); err != nil {
		return fmt.Errorf("could not set webhookParams: %w", err)
	}

	if err := jsVM.Set("webhookDedup", func(expression string) bool {
		return j.evaluateWebhookDedup(ctx, store, opts, expression)
	}); err != nil {
		return fmt.Errorf("could not set webhookDedup: %w", err)
	}

	return nil
}

func (j *JS) evaluateWebhookDedup(ctx context.Context, store storage.Driver, opts ExecuteOptions, expression string) bool {
	if opts.WebhookData == nil {
		return false // manual triggers are never duplicates
	}

	env := buildWebhookEnv(opts.WebhookData)

	keyHash, evalErr := filter.DedupKeyHash(expression, env)
	if evalErr != nil {
		slog.Error("webhookDedup evaluation failed", "error", evalErr, "expression", expression)

		return false // on error, don't skip
	}

	if keyHash == nil {
		return false // empty key, no dedup
	}

	ttl := opts.DedupTTL
	if ttl == 0 {
		ttl = defaultDedupTTL
	}

	cutoff := time.Now().UTC().Add(-ttl)
	if _, pruneErr := store.PruneWebhookDedup(ctx, cutoff); pruneErr != nil {
		slog.Warn("webhookDedup prune failed", "error", pruneErr)
	}

	isDup, checkErr := store.CheckWebhookDedup(ctx, opts.PipelineID, keyHash)
	if checkErr != nil {
		slog.Error("webhookDedup check failed", "error", checkErr)

		return false
	}

	if isDup {
		return true
	}

	if saveErr := store.SaveWebhookDedup(ctx, opts.PipelineID, keyHash); saveErr != nil {
		slog.Warn("webhookDedup save failed", "error", saveErr)
	}

	return false
}

// buildWebhookEnv constructs a filter.WebhookEnv from webhook data.
func buildWebhookEnv(wd *jsapi.WebhookData) filter.WebhookEnv {
	env := filter.WebhookEnv{
		Provider:  wd.Provider,
		EventType: wd.EventType,
		Method:    wd.Method,
		Headers:   wd.Headers,
		Query:     wd.Query,
		Body:      wd.Body,
	}

	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(wd.Body), &payload); jsonErr == nil {
		env.Payload = payload
	}

	return env
}

// setupTriggerPipeline registers the triggerPipeline() global function on the VM.
// It allows pipeline code to trigger other pipelines programmatically.
func (j *JS) setupTriggerPipeline(ctx context.Context, jsVM *goja.Runtime, rt *Runtime, opts ExecuteOptions) error {
	if opts.TriggerCallback == nil {
		// No callback provided -- register a no-op that returns an error.
		if err := jsVM.Set("triggerPipeline", func(_ goja.FunctionCall) goja.Value {
			panic(jsVM.NewGoError(errors.New("triggerPipeline is not available in this execution context")))
		}); err != nil {
			return fmt.Errorf("could not set triggerPipeline: %w", err)
		}

		return nil
	}

	triggerFn := func(call goja.FunctionCall) goja.Value {
		pipelineName := call.Argument(0).String()

		var jobs []string

		var args []string

		if optsArg := call.Argument(1); optsArg != nil && !goja.IsUndefined(optsArg) && !goja.IsNull(optsArg) {
			obj := optsArg.ToObject(jsVM)

			if jobsVal := obj.Get("jobs"); jobsVal != nil && !goja.IsUndefined(jobsVal) {
				if err := jsVM.ExportTo(jobsVal, &jobs); err != nil {
					panic(jsVM.NewGoError(fmt.Errorf("triggerPipeline: invalid jobs: %w", err)))
				}
			}

			if argsVal := obj.Get("args"); argsVal != nil && !goja.IsUndefined(argsVal) {
				if err := jsVM.ExportTo(argsVal, &args); err != nil {
					panic(jsVM.NewGoError(fmt.Errorf("triggerPipeline: invalid args: %w", err)))
				}
			}
		}

		promise, resolve, reject := jsVM.NewPromise()

		rt.promises.Add(1)

		go func() {
			runID, triggerErr := opts.TriggerCallback(ctx, pipelineName, jobs, args)

			rt.tasks <- func() error {
				defer rt.promises.Done()

				if triggerErr != nil {
					return reject(jsVM.NewGoError(triggerErr))
				}

				result := jsVM.NewObject()
				_ = result.Set("runID", runID)

				return resolve(result)
			}
		}()

		return jsVM.ToValue(promise)
	}

	if err := jsVM.Set("triggerPipeline", triggerFn); err != nil {
		return fmt.Errorf("could not set triggerPipeline: %w", err)
	}

	return nil
}

// setupPipelineContext registers the pipelineContext global on the VM.
func (j *JS) setupPipelineContext(jsVM *goja.Runtime, runtime *Runtime, driver orchestra.Driver, opts ExecuteOptions) error {
	triggeredBy := "manual"
	if opts.WebhookData != nil {
		triggeredBy = "webhook"
	}
	runtime.triggeredBy = triggeredBy

	args := opts.Args
	if args == nil {
		args = []string{}
	}

	targetJobs := opts.TargetJobs
	if targetJobs == nil {
		targetJobs = []string{}
	}

	pipelineContext := map[string]any{
		"runID":       opts.RunID,
		"pipelineID":  opts.PipelineID,
		"triggeredBy": triggeredBy,
		"args":        args,
		"targetJobs":  targetJobs,
	}
	if driver != nil {
		pipelineContext["driverName"] = driver.Name()
	}

	if err := jsVM.Set("pipelineContext", pipelineContext); err != nil {
		return fmt.Errorf("could not set pipelineContext: %w", err)
	}

	return nil
}

// runPipeline runs the compiled program, executes the pipeline function, and
// handles promise resolution and cleanup.
func (j *JS) runPipeline(ctx context.Context, jsVM *goja.Runtime, program *goja.Program, runtime *Runtime, r runner.Runner) error {
	pipeline, err := jsVM.RunProgram(program)
	if err != nil {
		defer jsVM.ClearInterrupt()

		return fmt.Errorf("could not run program: %w", err)
	}

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
		if resumable, ok := r.(*runner.ResumableRunner); ok {
			resumable.MarkInProgressAsAborted()
		}

		return fmt.Errorf("pipeline did not successfully execute: %w", err)
	}

	if ctx.Err() != nil {
		if resumable, ok := r.(*runner.ResumableRunner); ok {
			resumable.MarkInProgressAsAborted()
		}
	}

	if cleanupErr := r.CleanupVolumes(); cleanupErr != nil {
		j.logger.Error("volume.cleanup.failed", "err", cleanupErr)
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
	ctx    context.Context //nolint:containedctx // deliberate: JS VM cannot pass context parameters
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

func (w *storageContextWrapper) GetMostRecentJobStatus(pipelineID, jobName string) (string, error) {
	return w.driver.GetMostRecentJobStatus(w.ctx, pipelineID, jobName)
}

// Close wraps the storage Close method (no context needed).
func (w *storageContextWrapper) Close() error {
	return w.driver.Close()
}
