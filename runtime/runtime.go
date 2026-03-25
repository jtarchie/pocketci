package runtime

import (
	"context"
	"errors"
	"fmt"
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
	if len(call.Arguments) == 0 {
		return r.rejectImmediate(errors.New("run requires an input object"))
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	var input runner.RunInput
	if err := r.jsVM.ExportTo(inputObj, &input); err != nil {
		return r.rejectImmediate(fmt.Errorf("invalid input: %w", err))
	}

	if fn := extractJSCallback(r.jsVM, inputObj, "onOutput"); fn != nil {
		input.OnOutput = func(stream string, data string) {
			r.tasks <- func() error {
				_, _ = fn(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
				return nil
			}
		}
	}

	return r.jsVM.ToValue(asyncTask(r, "run", func() (*runner.RunResult, error) {
		return r.runner.Run(input)
	}, identity))
}

// StartSandbox starts a long-lived sandbox container and resolves with a JS object
// exposing exec(config) and close() methods.
func (r *Runtime) StartSandbox(call goja.FunctionCall) goja.Value {
	if len(call.Arguments) == 0 {
		return r.rejectImmediate(errors.New("startSandbox requires an input object"))
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	var input runner.SandboxInput
	if err := r.jsVM.ExportTo(inputObj, &input); err != nil {
		return r.rejectImmediate(fmt.Errorf("invalid startSandbox input: %w", err))
	}

	return r.jsVM.ToValue(asyncTask(r, "startSandbox", func() (*runner.SandboxHandle, error) {
		return r.runner.StartSandbox(input)
	}, func(handle *runner.SandboxHandle) (any, error) {
		return r.buildSandboxObject(handle), nil
	}))
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
		if len(call.Arguments) == 0 {
			return r.rejectImmediate(errors.New("exec requires an input object"))
		}

		execInputObj := call.Arguments[0].ToObject(r.jsVM)

		var execInput runner.ExecInput
		if err := r.jsVM.ExportTo(execInputObj, &execInput); err != nil {
			return r.rejectImmediate(fmt.Errorf("invalid exec input: %w", err))
		}

		if fn := extractJSCallback(r.jsVM, execInputObj, "onOutput"); fn != nil {
			execInput.OnOutput = func(stream, data string) {
				r.tasks <- func() error {
					_, _ = fn(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
					return nil
				}
			}
		}

		return r.jsVM.ToValue(asyncTask(r, "sandbox.exec", func() (*runner.RunResult, error) {
			return handle.Exec(r.ctx, execInput)
		}, identity))
	}
}

// sandboxCloseFunc returns the JS-callable close method for a sandbox handle.
func (r *Runtime) sandboxCloseFunc(handle *runner.SandboxHandle) func(goja.FunctionCall) goja.Value {
	return func(_ goja.FunctionCall) goja.Value {
		return r.jsVM.ToValue(asyncTask(r, "sandbox.close", func() (struct{}, error) {
			return struct{}{}, handle.Close()
		}, func(_ struct{}) (any, error) {
			return goja.Undefined(), nil
		}))
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
