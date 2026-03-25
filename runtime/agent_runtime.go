package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

	if len(call.Arguments) == 0 {
		return r.rejectImmediate(errors.New("agent.run requires an input object"))
	}

	inputObj := call.Arguments[0].ToObject(r.jsVM)

	var config agent.AgentConfig
	if err := r.jsVM.ExportTo(inputObj, &config); err != nil {
		return r.rejectImmediate(fmt.Errorf("invalid agent input: %w", err))
	}

	ar.extractAgentCallbacks(inputObj, &config)

	return r.jsVM.ToValue(asyncTask(r, "agent.run", func() (json.RawMessage, error) {
		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		ar.prepareAgentConfig(&config)
		ar.installAgentFunc(ctx, &config)

		configJSON, err := json.Marshal(config)
		if err != nil {
			return nil, fmt.Errorf("could not marshal agent config: %w", err)
		}

		return r.runner.RunAgent(configJSON)
	}, func(resultJSON json.RawMessage) (any, error) {
		var result agent.AgentResult
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			return nil, fmt.Errorf("could not unmarshal agent result: %w", err)
		}

		return result, nil
	}))
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

