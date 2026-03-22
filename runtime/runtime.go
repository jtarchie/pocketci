package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/dop251/goja"

	"github.com/jtarchie/pocketci/runtime/agent"
	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

type Runtime struct {
	jsVM           *goja.Runtime
	promises       *sync.WaitGroup
	runner         runner.Runner
	tasks          chan func() error
	namespace      string
	runID          string
	mu             sync.Mutex // Protects volumeIndex
	volumeIndex    int        // Counter for unnamed volumes
	secretsManager secrets.Manager
	pipelineID     string
	ctx            context.Context //nolint: containedctx
	storage        storage.Driver
	triggeredBy    string
}

func NewRuntime(
	jsVM *goja.Runtime,
	runner runner.Runner,
	namespace string,
	runID string,
) *Runtime {
	promises := &sync.WaitGroup{}
	tasks := make(chan func() error, 1)

	return &Runtime{
		jsVM:      jsVM,
		promises:  promises,
		runner:    runner,
		tasks:     tasks,
		namespace: namespace,
		runID:     runID,
	}
}

// Run executes a container task. Accepts an object with optional onOutput callback.
func (r *Runtime) Run(call goja.FunctionCall) goja.Value {
	promise, resolve, reject := r.jsVM.NewPromise()

	// Extract input from the first argument
	if len(call.Arguments) == 0 {
			_ = reject(r.jsVM.NewGoError(errors.New("run requires an input object")))
		return r.jsVM.ToValue(promise)
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	// Parse the input struct (goja will map fields by json tags)
	var input runner.RunInput
	if err := r.jsVM.ExportTo(inputObj, &input); err != nil {
		_ = reject(r.jsVM.NewGoError(fmt.Errorf("invalid input: %w", err)))
		return r.jsVM.ToValue(promise)
	}

	// Check for onOutput callback
	onOutputVal := inputObj.Get("onOutput")
	var onOutputFunc goja.Callable
	if onOutputVal != nil && !goja.IsUndefined(onOutputVal) && !goja.IsNull(onOutputVal) {
		var ok bool
		onOutputFunc, ok = goja.AssertFunction(onOutputVal)
		if !ok {
				_ = reject(r.jsVM.NewGoError(errors.New("onOutput must be a function")))
			return r.jsVM.ToValue(promise)
		}
	}

	// If callback provided, wrap it to safely invoke through the tasks channel
	if onOutputFunc != nil {
		input.OnOutput = func(stream string, data string) {
			// Queue the callback invocation on the main JS thread via tasks channel
			r.tasks <- func() error {
				_, err := onOutputFunc(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
				if err != nil {
					// Log but don't fail - callbacks shouldn't break the task
					return nil
				}
				return nil
			}
		}
	}

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("runtime.run.panic", "panic", p, "stack", string(debug.Stack()))
				r.tasks <- func() error {
					defer r.promises.Done()
					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in run: %v", p)))
				}
			}
		}()

		result, err := r.runner.Run(input)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				err = reject(err)
				if err != nil {
					return fmt.Errorf("could not reject run: %w", err)
				}

				return nil
			}

			err := resolve(result)
			if err != nil {
				return fmt.Errorf("could not resolve run: %w", err)
			}

			return nil
		}
	}()

	return r.jsVM.ToValue(promise)
}

func (r *Runtime) CreateVolume(input runner.VolumeInput) *goja.Promise {
	if input.Name == "" {
		// Generate deterministic volume name using counter
		r.mu.Lock()
		volumeID := fmt.Sprintf("vol-%d", r.volumeIndex)
		r.volumeIndex++
		r.mu.Unlock()
		input.Name = support.DeterministicVolumeID(r.namespace, fmt.Sprintf("%s-%s", r.runID, volumeID))
	}

	promise, resolve, reject := r.jsVM.NewPromise()

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("runtime.createVolume.panic", "panic", p, "stack", string(debug.Stack()))
				r.tasks <- func() error {
					defer r.promises.Done()
					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in createVolume: %v", p)))
				}
			}
		}()

		result, err := r.runner.CreateVolume(input)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				err = reject(err)
				if err != nil {
					return fmt.Errorf("could not reject run: %w", err)
				}

				return nil
			}

			err := resolve(result)
			if err != nil {
				return fmt.Errorf("could not resolve create volume: %w", err)
			}

			return nil
		}
	}()

	return promise
}

// StartSandbox starts a long-lived sandbox container and resolves with a JS object
// exposing exec(config) and close() methods.
func (r *Runtime) StartSandbox(call goja.FunctionCall) goja.Value {
	promise, resolve, reject := r.jsVM.NewPromise()

	if len(call.Arguments) == 0 {
		_ = reject(r.jsVM.NewGoError(errors.New("startSandbox requires an input object")))
		return r.jsVM.ToValue(promise)
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	var input runner.SandboxInput
	if err := r.jsVM.ExportTo(inputObj, &input); err != nil {
		_ = reject(r.jsVM.NewGoError(fmt.Errorf("invalid startSandbox input: %w", err)))
		return r.jsVM.ToValue(promise)
	}

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("runtime.startSandbox.panic", "panic", p, "stack", string(debug.Stack()))
				r.tasks <- func() error {
					defer r.promises.Done()
					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in startSandbox: %v", p)))
				}
			}
		}()

		handle, err := r.runner.StartSandbox(input)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				err = reject(err)
				if err != nil {
					return fmt.Errorf("could not reject startSandbox: %w", err)
				}

				return nil
			}

			// Build the sandbox JS object with exec and close methods.
			sandboxObj := r.jsVM.NewObject()
			_ = sandboxObj.Set("id", handle.ID())

			_ = sandboxObj.Set("exec", func(call goja.FunctionCall) goja.Value {
				execPromise, execResolve, execReject := r.jsVM.NewPromise()

				if len(call.Arguments) == 0 {
					_ = execReject(r.jsVM.NewGoError(errors.New("exec requires an input object")))
					return r.jsVM.ToValue(execPromise)
				}

				execInputObj := call.Arguments[0].ToObject(r.jsVM)

				var execInput runner.ExecInput
				if err := r.jsVM.ExportTo(execInputObj, &execInput); err != nil {
					_ = execReject(r.jsVM.NewGoError(fmt.Errorf("invalid exec input: %w", err)))
					return r.jsVM.ToValue(execPromise)
				}

				// Check for onOutput callback.
				onOutputVal := execInputObj.Get("onOutput")
				if onOutputVal != nil && !goja.IsUndefined(onOutputVal) && !goja.IsNull(onOutputVal) {
					if onOutputFunc, ok := goja.AssertFunction(onOutputVal); ok {
						execInput.OnOutput = func(stream, data string) {
							r.tasks <- func() error {
								_, err := onOutputFunc(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
								if err != nil {
									return nil
								}

								return nil
							}
						}
					}
				}

				r.promises.Add(1)

				go func() {
					defer func() {
						if p := recover(); p != nil {
							slog.Error("runtime.sandbox.exec.panic", "panic", p, "stack", string(debug.Stack()))
							r.tasks <- func() error {
								defer r.promises.Done()
								return execReject(r.jsVM.NewGoError(fmt.Errorf("panic in sandbox exec: %v", p)))
							}
						}
					}()

					result, err := handle.Exec(execInput)

					r.tasks <- func() error {
						defer r.promises.Done()

						if err != nil {
							err = execReject(err)
							if err != nil {
								return fmt.Errorf("could not reject exec: %w", err)
							}

							return nil
						}

						err = execResolve(result)
						if err != nil {
							return fmt.Errorf("could not resolve exec: %w", err)
						}

						return nil
					}
				}()

				return r.jsVM.ToValue(execPromise)
			})

			_ = sandboxObj.Set("close", func(call goja.FunctionCall) goja.Value {
				closePromise, closeResolve, closeReject := r.jsVM.NewPromise()

				r.promises.Add(1)

				go func() {
					defer func() {
						if p := recover(); p != nil {
							slog.Error("runtime.sandbox.close.panic", "panic", p, "stack", string(debug.Stack()))
							r.tasks <- func() error {
								defer r.promises.Done()
								return closeReject(r.jsVM.NewGoError(fmt.Errorf("panic in sandbox close: %v", p)))
							}
						}
					}()

					err := handle.Close()

					r.tasks <- func() error {
						defer r.promises.Done()

						if err != nil {
							err = closeReject(err)
							if err != nil {
								return fmt.Errorf("could not reject close: %w", err)
							}

							return nil
						}

						err = closeResolve(goja.Undefined())
						if err != nil {
							return fmt.Errorf("could not resolve close: %w", err)
						}

						return nil
					}
				}()

				return r.jsVM.ToValue(closePromise)
			})

			err = resolve(sandboxObj)
			if err != nil {
				return fmt.Errorf("could not resolve startSandbox: %w", err)
			}

			return nil
		}
	}()

	return r.jsVM.ToValue(promise)
}

// Agent runs an LLM agent step. Accepts an object with prompt, model, image,
// mounts, outputVolumePath, and an optional onOutput callback.
func (r *Runtime) Agent(call goja.FunctionCall) goja.Value {
	promise, resolve, reject := r.jsVM.NewPromise()

	if len(call.Arguments) == 0 {
		_ = reject(r.jsVM.NewGoError(errors.New("agent requires an input object")))
		return r.jsVM.ToValue(promise)
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	var config agent.AgentConfig
	if err := r.jsVM.ExportTo(inputObj, &config); err != nil {
		_ = reject(r.jsVM.NewGoError(fmt.Errorf("invalid agent input: %w", err)))
		return r.jsVM.ToValue(promise)
	}

	// Extract optional onOutput callback.
	onOutputVal := inputObj.Get("onOutput")
	if onOutputVal != nil && !goja.IsUndefined(onOutputVal) && !goja.IsNull(onOutputVal) {
		if onOutputFunc, ok := goja.AssertFunction(onOutputVal); ok {
			config.OnOutput = func(stream, data string) {
				r.tasks <- func() error {
					_, _ = onOutputFunc(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
					return nil
				}
			}
		}
	}

	// Extract optional onUsage callback.
	onUsageVal := inputObj.Get("onUsage")
	if onUsageVal != nil && !goja.IsUndefined(onUsageVal) && !goja.IsNull(onUsageVal) {
		if onUsageFunc, ok := goja.AssertFunction(onUsageVal); ok {
			config.OnUsage = func(usage agent.AgentUsage) {
				r.tasks <- func() error {
					_, _ = onUsageFunc(goja.Undefined(), r.jsVM.ToValue(usage))
					return nil
				}
			}
		}
	}

	// Extract optional onAuditEvent callback.
	onAuditEventVal := inputObj.Get("onAuditEvent")
	if onAuditEventVal != nil && !goja.IsUndefined(onAuditEventVal) && !goja.IsNull(onAuditEventVal) {
		if onAuditEventFunc, ok := goja.AssertFunction(onAuditEventVal); ok {
			config.OnAuditEvent = func(event agent.AuditEvent) {
				r.tasks <- func() error {
					_, _ = onAuditEventFunc(goja.Undefined(), r.jsVM.ToValue(event))
					return nil
				}
			}
		}
	}

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("runtime.agent.panic", "panic", p, "stack", string(debug.Stack()))
				r.tasks <- func() error {
					defer r.promises.Done()
					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in agent: %v", p)))
				}
			}
		}()

		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		// Populate runtime context into config before calling RunAgent.
		config.Storage = r.storage
		config.Namespace = r.namespace
		config.RunID = r.runID
		config.TriggeredBy = r.triggeredBy

		// Set the AgentFunc on the runner so that ResumableRunner can track
		// and cache agent results. The func captures per-call context
		// (callbacks, secrets, storage, etc.) and delegates to agent.RunAgent.
		r.runner.SetAgentFunc(func(configJSON json.RawMessage) (json.RawMessage, error) {
			// Unmarshal the serializable parts of config, keeping the
			// non-serialisable fields (callbacks, Storage, etc.) from the
			// outer config closure.
			var serializableConfig agent.AgentConfig
			if err := json.Unmarshal(configJSON, &serializableConfig); err != nil {
				return nil, fmt.Errorf("could not unmarshal agent config: %w", err)
			}

			// Merge: serializable fields from JSON, non-serializable from closure.
			serializableConfig.OnOutput = config.OnOutput
			serializableConfig.OnAuditEvent = config.OnAuditEvent
			serializableConfig.OnUsage = config.OnUsage
			serializableConfig.Storage = config.Storage
			serializableConfig.Namespace = config.Namespace
			serializableConfig.RunID = config.RunID
			serializableConfig.PipelineID = config.PipelineID
			serializableConfig.TriggeredBy = config.TriggeredBy

			result, err := agent.RunAgent(ctx, r.runner, r.secretsManager, r.pipelineID, serializableConfig)
			if err != nil {
				return nil, err
			}

			resultJSON, err := json.Marshal(result)
			if err != nil {
				return nil, fmt.Errorf("could not marshal agent result: %w", err)
			}

			return resultJSON, nil
		})

		// Marshal the serializable config to JSON for the runner interface.
		configJSON, err := json.Marshal(config)
		if err != nil {
			r.tasks <- func() error {
				defer r.promises.Done()

				return reject(r.jsVM.NewGoError(fmt.Errorf("could not marshal agent config: %w", err)))
			}

			return
		}

		resultJSON, err := r.runner.RunAgent(configJSON)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				err = reject(r.jsVM.NewGoError(err))
				if err != nil {
					return fmt.Errorf("could not reject agent: %w", err)
				}

				return nil
			}

			var result agent.AgentResult
			if unmarshalErr := json.Unmarshal(resultJSON, &result); unmarshalErr != nil {
				return reject(r.jsVM.NewGoError(fmt.Errorf("could not unmarshal agent result: %w", unmarshalErr)))
			}

			err = resolve(result)
			if err != nil {
				return fmt.Errorf("could not resolve agent: %w", err)
			}

			return nil
		}
	}()

	return r.jsVM.ToValue(promise)
}

// ReadFilesFromVolume reads specific files from a named volume.
// Returns a Promise that resolves to a map of path → content strings.
func (r *Runtime) ReadFilesFromVolume(call goja.FunctionCall) goja.Value {
	promise, resolve, reject := r.jsVM.NewPromise()

	if len(call.Arguments) < 2 {
		_ = reject(r.jsVM.NewGoError(errors.New("readFilesFromVolume requires volumeName and at least one filePath")))
		return r.jsVM.ToValue(promise)
	}

	volumeName := call.Arguments[0].String()

	filePaths := make([]string, 0, len(call.Arguments)-1)
	for i := 1; i < len(call.Arguments); i++ {
		filePaths = append(filePaths, call.Arguments[i].String())
	}

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("runtime.readFilesFromVolume.panic", "panic", p, "stack", string(debug.Stack()))
				r.tasks <- func() error {
					defer r.promises.Done()
					return reject(r.jsVM.NewGoError(fmt.Errorf("panic in readFilesFromVolume: %v", p)))
				}
			}
		}()

		result, err := r.runner.ReadFilesFromVolume(volumeName, filePaths...)

		r.tasks <- func() error {
			defer r.promises.Done()

			if err != nil {
				err = reject(r.jsVM.NewGoError(err))
				if err != nil {
					return fmt.Errorf("could not reject readFilesFromVolume: %w", err)
				}

				return nil
			}

			err = resolve(result)
			if err != nil {
				return fmt.Errorf("could not resolve readFilesFromVolume: %w", err)
			}

			return nil
		}
	}()

	return r.jsVM.ToValue(promise)
}

func (r *Runtime) Wait() error {
	go func() {
		r.promises.Wait()
		close(r.tasks)
	}()

	for task := range r.tasks {
		err := task()
		if err != nil {
			return fmt.Errorf("could not wait: %w", err)
		}
	}

	return nil
}
