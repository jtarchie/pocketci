package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/dop251/goja"

	"github.com/jtarchie/pocketci/runtime/runner"
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

// Volumes returns a Volumes instance sharing this runtime's state.
func (r *Runtime) Volumes() *Volumes {
	return &Volumes{rt: r}
}

// AgentRT returns an AgentRuntime instance sharing this runtime's state.
func (r *Runtime) AgentRT() *AgentRuntime {
	return &AgentRuntime{rt: r}
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

			sandboxObj := r.buildSandboxObject(handle)

			err = resolve(sandboxObj)
			if err != nil {
				return fmt.Errorf("could not resolve startSandbox: %w", err)
			}

			return nil
		}
	}()

	return r.jsVM.ToValue(promise)
}

// buildSandboxObject constructs the JS object with exec and close methods
// that is returned by startSandbox.
func (r *Runtime) buildSandboxObject(handle *runner.SandboxHandle) *goja.Object {
	sandboxObj := r.jsVM.NewObject()
	_ = sandboxObj.Set("id", handle.ID())
	_ = sandboxObj.Set("exec", r.sandboxExecFunc(handle))
	_ = sandboxObj.Set("close", r.sandboxCloseFunc(handle))

	return sandboxObj
}

// sandboxExecFunc returns the JS-callable exec method for a sandbox handle.
func (r *Runtime) sandboxExecFunc(handle *runner.SandboxHandle) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
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

		r.bindOnOutputCallback(execInputObj, &execInput)

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

			result, err := handle.Exec(r.ctx, execInput)

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
	}
}

// bindOnOutputCallback extracts the onOutput JS callback from the exec input
// object and wires it into the ExecInput's OnOutput field.
func (r *Runtime) bindOnOutputCallback(execInputObj *goja.Object, execInput *runner.ExecInput) {
	onOutputVal := execInputObj.Get("onOutput")
	if onOutputVal == nil || goja.IsUndefined(onOutputVal) || goja.IsNull(onOutputVal) {
		return
	}

	onOutputFunc, ok := goja.AssertFunction(onOutputVal)
	if !ok {
		return
	}

	execInput.OnOutput = func(stream, data string) {
		r.tasks <- func() error {
			_, _ = onOutputFunc(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
			return nil
		}
	}
}

// sandboxCloseFunc returns the JS-callable close method for a sandbox handle.
func (r *Runtime) sandboxCloseFunc(handle *runner.SandboxHandle) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
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
	}
}

// Deprecated: use volumes.create() instead.
func (r *Runtime) CreateVolume(input runner.VolumeInput) *goja.Promise {
	return r.Volumes().Create(input)
}

// Deprecated: use volumes.readFiles() instead.
func (r *Runtime) ReadFilesFromVolume(call goja.FunctionCall) goja.Value {
	return r.Volumes().ReadFiles(call)
}

// Deprecated: use agent.run() instead.
func (r *Runtime) Agent(call goja.FunctionCall) goja.Value {
	return r.AgentRT().Run(call)
}

func (r *Runtime) Wait() error {
	go func() {
		r.promises.Wait()
		close(r.tasks)
	}()

	var firstErr error

	for task := range r.tasks {
		err := task()
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("could not wait: %w", err)
		}
	}

	return firstErr
}
