package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/dop251/goja"

	"github.com/jtarchie/pocketci/runtime/agent"
)

// AgentRuntime exposes LLM agent execution to JavaScript as the `agent` namespace.
// It holds a reference to the parent Runtime to access shared state that may be
// set after construction (e.g. triggeredBy).
type AgentRuntime struct {
	rt *Runtime
}

// Run runs an LLM agent step. Accepts an object with prompt, model, image,
// mounts, outputVolumePath, and optional callbacks.
func (ar *AgentRuntime) Run(call goja.FunctionCall) goja.Value {
	r := ar.rt
	promise, resolve, reject := r.jsVM.NewPromise()

	if len(call.Arguments) == 0 {
		_ = reject(r.jsVM.NewGoError(errors.New("agent.run requires an input object")))
		return r.jsVM.ToValue(promise)
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	var config agent.AgentConfig
	if err := r.jsVM.ExportTo(inputObj, &config); err != nil {
		_ = reject(r.jsVM.NewGoError(fmt.Errorf("invalid agent input: %w", err)))
		return r.jsVM.ToValue(promise)
	}

	ar.extractAgentCallbacks(inputObj, &config)

	r.promises.Add(1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("agent.run.panic", "panic", p, "stack", string(debug.Stack()))
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

		ar.prepareAgentConfig(&config)
		ar.installAgentFunc(ctx, &config)

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

// extractAgentCallbacks reads optional JS callback properties from the input
// object and wires them into the agent config.
func (ar *AgentRuntime) extractAgentCallbacks(inputObj *goja.Object, config *agent.AgentConfig) {
	r := ar.rt

	if fn := extractJSCallback(r.jsVM, inputObj, "onOutput"); fn != nil {
		config.OnOutput = func(stream, data string) {
			r.tasks <- func() error {
				_, _ = fn(goja.Undefined(), r.jsVM.ToValue(stream), r.jsVM.ToValue(data))
				return nil
			}
		}
	}

	if fn := extractJSCallback(r.jsVM, inputObj, "onUsage"); fn != nil {
		config.OnUsage = func(usage agent.AgentUsage) {
			r.tasks <- func() error {
				_, _ = fn(goja.Undefined(), r.jsVM.ToValue(usage))
				return nil
			}
		}
	}

	if fn := extractJSCallback(r.jsVM, inputObj, "onAuditEvent"); fn != nil {
		config.OnAuditEvent = func(event agent.AuditEvent) {
			r.tasks <- func() error {
				_, _ = fn(goja.Undefined(), r.jsVM.ToValue(event))
				return nil
			}
		}
	}
}

// prepareAgentConfig populates runtime context into the agent config.
func (ar *AgentRuntime) prepareAgentConfig(config *agent.AgentConfig) {
	r := ar.rt
	config.Storage = r.storage
	config.Namespace = r.namespace
	config.RunID = r.runID
	config.TriggeredBy = r.triggeredBy
}

// installAgentFunc sets the AgentFunc on the runner so that ResumableRunner
// can track and cache agent results.
func (ar *AgentRuntime) installAgentFunc(ctx context.Context, config *agent.AgentConfig) {
	r := ar.rt
	r.runner.SetAgentFunc(func(configJSON json.RawMessage) (json.RawMessage, error) {
		var serializableConfig agent.AgentConfig
		if err := json.Unmarshal(configJSON, &serializableConfig); err != nil {
			return nil, fmt.Errorf("could not unmarshal agent config: %w", err)
		}

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
}

// extractJSCallback reads a named property from a goja object and returns the
// callable if it exists and is a function; otherwise returns nil.
func extractJSCallback(vm *goja.Runtime, obj *goja.Object, name string) goja.Callable {
	val := obj.Get(name)
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil
	}

	fn, ok := goja.AssertFunction(val)
	if !ok {
		return nil
	}

	return fn
}
