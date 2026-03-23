package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/jtarchie/pocketci/runtime/agent/internal/helpers"
	agentmodel "github.com/jtarchie/pocketci/runtime/agent/model"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
)

// callAgentInput is the tool input schema for sub-agent tools (both modes).
// The LLM passes a plain-text request to the sub-agent.
type callAgentInput struct {
	Request string `json:"request"`
}

// callAgentOutput is the tool result schema for own-container sub-agent calls.
type callAgentOutput struct {
	Result string `json:"result"`
	Status string `json:"status"`
}

// buildSubAgentTool creates an ADK tool for the given sub-agent configuration.
//
// Shared-container mode (sub-agent image matches or is empty): wraps an ADK
// llmagent as an agenttool so the parent LLM can call it directly.
//
// Own-container mode (sub-agent declares a different image): registers a
// functiontool that spins up a separate sandbox, runs the sub-agent to
// completion, persists results to a nested storage path, and returns the
// final text to the parent.
func buildSubAgentTool(
	ctx context.Context,
	sandbox *pipelinerunner.SandboxHandle,
	sandboxRunner pipelinerunner.Runner,
	sm secrets.Manager,
	pipelineID string,
	subCfg SubAgentConfig,
	parentConfig AgentConfig,
) (adktool.Tool, error) {
	subImage := subCfg.Image
	if subImage == "" {
		subImage = parentConfig.Image
	}

	subModel := subCfg.Model
	if subModel == "" {
		subModel = parentConfig.Model
	}

	if subImage != parentConfig.Image {
		// Own-container: custom functiontool that spins up a separate sandbox.
		return newCallAgentTool(ctx, sandboxRunner, sm, pipelineID, subCfg, subModel, parentConfig)
	}

	// Shared-container: build a functiontool that runs the sub-agent in
	// its own ADK session (reusing the parent's sandbox), collects the
	// full audit log + usage, persists to storage, and returns the result.
	provider, modelName := agentmodel.SplitModel(subModel)

	apiKey := agentmodel.ResolveSecret(ctx, sm, pipelineID, "agent/"+provider)
	if apiKey == "" {
		envKey := strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
		apiKey = os.Getenv(envKey)
	}

	subLLM, err := agentmodel.Resolve(provider, modelName, apiKey, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve model: %w", err)
	}

	// Sub-agent reuses the same sandbox tools as the parent.
	subRunScript, err := newRunScriptTool(sandbox, parentConfig.OnOutput) //nolint:contextcheck // ctx flows via adktool.Context at execution time
	if err != nil {
		return nil, fmt.Errorf("run_script tool: %w", err)
	}

	subReadFile, err := newReadFileTool(sandbox, parentConfig.OnOutput) //nolint:contextcheck // ctx flows via adktool.Context at execution time
	if err != nil {
		return nil, fmt.Errorf("read_file tool: %w", err)
	}

	subListTasks, err := newListTasksTool(ctx, parentConfig)
	if err != nil {
		return nil, fmt.Errorf("list_tasks tool: %w", err)
	}

	subGetTaskResult, err := newGetTaskResultTool(ctx, parentConfig)
	if err != nil {
		return nil, fmt.Errorf("get_task_result tool: %w", err)
	}

	subAgent, err := llmagent.New(llmagent.Config{
		Name:        subCfg.Name,
		Model:       subLLM,
		Description: fmt.Sprintf("Specialist sub-agent: %s. Call this when you need its expertise.", subCfg.Name),
		Instruction: subCfg.Prompt,
		Tools:       []adktool.Tool{subRunScript, subReadFile, subListTasks, subGetTaskResult},
	})
	if err != nil {
		return nil, fmt.Errorf("create sub-agent: %w", err)
	}

	return newSharedContainerSubAgentTool(ctx, subCfg, subAgent, parentConfig)
}

// newSharedContainerSubAgentTool builds a functiontool that runs a sub-agent
// in its own ADK session while reusing the parent's sandbox container and tools.
// It collects the full audit log, usage, and final text, then persists them to
// {storageKeyPrefix}/sub-agents/{name}/run so the UI and MCP tools can access
// each sub-agent's results individually.
func newSharedContainerSubAgentTool(
	ctx context.Context,
	subCfg SubAgentConfig,
	subAgent agent.Agent,
	parentConfig AgentConfig,
) (adktool.Tool, error) {
	return functiontool.New[callAgentInput, callAgentOutput](
		functiontool.Config{
			Name:        subCfg.Name,
			Description: fmt.Sprintf("Specialist sub-agent: %s. Call this when you need its expertise.", subCfg.Name),
		},
		func(_ adktool.Context, input callAgentInput) (callAgentOutput, error) {
			return executeSharedSubAgent(ctx, subCfg, subAgent, parentConfig, input)
		},
	)
}

// executeSharedSubAgent runs a sub-agent in its own ADK session, collects
// the full audit log, usage, and final text, then persists everything
// to storage.
func executeSharedSubAgent(
	ctx context.Context,
	subCfg SubAgentConfig,
	subAgent agent.Agent,
	parentConfig AgentConfig,
	input callAgentInput,
) (callAgentOutput, error) {
	prompt := subCfg.Prompt
	if input.Request != "" {
		if prompt != "" {
			prompt = prompt + "\n\nSpecific request: " + input.Request
		} else {
			prompt = input.Request
		}
	}

	startedAt := time.Now().UTC()

	sessionService := session.InMemoryService()

	sessResp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "ci-sub-agent",
		UserID:  "pipeline",
	})
	if err != nil {
		return callAgentOutput{}, fmt.Errorf("create session: %w", err)
	}

	runnr, err := runner.New(runner.Config{
		AppName:        "ci-sub-agent",
		Agent:          subAgent,
		SessionService: sessionService,
	})
	if err != nil {
		return callAgentOutput{}, fmt.Errorf("create runner: %w", err)
	}

	var textBuilder strings.Builder
	var resultBuilder strings.Builder
	var auditEvents []AuditEvent
	var usage AgentUsage

	AppendAuditEvent(&auditEvents, AuditEvent{
		Timestamp: startedAt.Format(time.RFC3339),
		Author:    "user",
		Type:      "user_message",
		Text:      prompt,
	}, nil)

	userMsg := genai.NewContentFromText(prompt, genai.RoleUser)

	for event, err := range runnr.Run(ctx, "pipeline", sessResp.Session.ID(), userMsg, agent.RunConfig{}) {
		if err != nil {
			return callAgentOutput{}, fmt.Errorf("sub-agent run: %w", err)
		}

		processSubAgentEvent(event, &textBuilder, &resultBuilder, &auditEvents, &usage)

		if event.UsageMetadata != nil {
			persistSubAgentProgress(ctx, subCfg, parentConfig, "running", textBuilder.String(), usage, auditEvents, startedAt)
		}
	}

	finalText := resultBuilder.String()
	if finalText == "" {
		runSubAgentFollowUp(ctx, runnr, sessResp.Session.ID(), &textBuilder, &resultBuilder, &auditEvents, &usage, subCfg, parentConfig, startedAt)
		finalText = resultBuilder.String()
	}

	if finalText == "" {
		finalText = textBuilder.String()
	}

	status := "success"
	if finalText == "" && usage.ToolCallCount == 0 && usage.LLMRequests == 0 {
		status = "error"
	}

	persistSubAgentProgress(ctx, subCfg, parentConfig, status, finalText, usage, auditEvents, startedAt)

	return callAgentOutput{
		Result: finalText,
		Status: status,
	}, nil
}

// processSubAgentEvent handles a single event from the sub-agent runner,
// accumulating text, audit events, and usage statistics.
func processSubAgentEvent(
	event *session.Event,
	textBuilder, resultBuilder *strings.Builder,
	auditEvents *[]AuditEvent,
	usage *AgentUsage,
) {
	if event.UsageMetadata != nil {
		accumulateUsage(usage, event.UsageMetadata, nil)
	}

	if event.Content == nil {
		return
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	if !event.Timestamp.IsZero() {
		ts = event.Timestamp.UTC().Format(time.RFC3339)
	}

	isFinal := event.IsFinalResponse()

	for _, part := range event.Content.Parts {
		if part.FunctionCall != nil {
			fc := part.FunctionCall
			usage.ToolCallCount++

			AppendAuditEvent(auditEvents, AuditEvent{
				Timestamp:    ts,
				InvocationID: event.InvocationID,
				Author:       event.Author,
				Type:         "tool_call",
				ToolName:     fc.Name,
				ToolCallID:   fc.ID,
				ToolArgs:     fc.Args,
			}, nil)
		}

		if part.FunctionResponse != nil {
			fr := part.FunctionResponse

			AppendAuditEvent(auditEvents, AuditEvent{
				Timestamp:    ts,
				InvocationID: event.InvocationID,
				Author:       event.Author,
				Type:         "tool_response",
				ToolName:     fr.Name,
				ToolCallID:   fr.ID,
				ToolResult:   fr.Response,
			}, nil)
		}

		if part.Text == "" {
			continue
		}

		eventType := "model_text"
		if isFinal {
			eventType = "model_final"
		}

		AppendAuditEvent(auditEvents, AuditEvent{
			Timestamp:    ts,
			InvocationID: event.InvocationID,
			Author:       event.Author,
			Type:         eventType,
			Text:         part.Text,
		}, nil)

		textBuilder.WriteString(part.Text)

		if isFinal {
			resultBuilder.WriteString(part.Text)
		}
	}
}

// persistSubAgentProgress writes the sub-agent's current state to storage so
// the UI and MCP tools can display progress or final results.
func persistSubAgentProgress(
	ctx context.Context,
	subCfg SubAgentConfig,
	parentConfig AgentConfig,
	status string,
	text string,
	usage AgentUsage,
	auditEvents []AuditEvent,
	startedAt time.Time,
) {
	if subCfg.StorageKeyPrefix == "" || parentConfig.Storage == nil {
		return
	}

	storageKey := subCfg.StorageKeyPrefix + "/sub-agents/" + subCfg.Name + "/run"
	_ = parentConfig.Storage.Set(ctx, storageKey, map[string]any{
		"status":     status,
		"stdout":     text,
		"usage":      usage,
		"audit_log":  auditEvents,
		"started_at": startedAt.Format(time.RFC3339),
		"elapsed":    helpers.FormatDuration(time.Since(startedAt)),
	})
}

// runSubAgentFollowUp sends a follow-up message to a sub-agent that used tools
// but never produced a final text response, asking it to summarize its findings.
func runSubAgentFollowUp(
	ctx context.Context,
	runnr *runner.Runner,
	sessionID string,
	textBuilder, resultBuilder *strings.Builder,
	auditEvents *[]AuditEvent,
	usage *AgentUsage,
	subCfg SubAgentConfig,
	parentConfig AgentConfig,
	startedAt time.Time,
) {
	followUpMsg := genai.NewContentFromText(
		"You have completed your tool calls. Now provide your complete "+
			"response summarizing your findings.", genai.RoleUser)

	for event, err := range runnr.Run(ctx, "pipeline", sessionID, followUpMsg, agent.RunConfig{}) {
		if err != nil {
			break
		}

		processSubAgentEvent(event, textBuilder, resultBuilder, auditEvents, usage)

		if event.UsageMetadata != nil {
			persistSubAgentProgress(ctx, subCfg, parentConfig, "running", textBuilder.String(), *usage, *auditEvents, startedAt)
		}
	}
}

// newCallAgentTool builds a functiontool that runs a sub-agent in its own
// sandbox container. Used when the sub-agent's image differs from the parent's.
// Results are persisted at {storageKeyPrefix}/sub-agents/{name}/run so the
// UI automatically shows them nested under the parent agent step.
func newCallAgentTool(
	ctx context.Context,
	sandboxRunner pipelinerunner.Runner,
	sm secrets.Manager,
	pipelineID string,
	subCfg SubAgentConfig,
	subModel string,
	parentConfig AgentConfig,
) (adktool.Tool, error) {
	return functiontool.New[callAgentInput, callAgentOutput](
		functiontool.Config{
			Name:        subCfg.Name,
			Description: fmt.Sprintf("Specialist sub-agent: %s. Call this when you need its expertise.", subCfg.Name),
		},
		func(_ adktool.Context, input callAgentInput) (callAgentOutput, error) {
			prompt := subCfg.Prompt
			if input.Request != "" {
				if prompt != "" {
					prompt = prompt + "\n\nSpecific request: " + input.Request
				} else {
					prompt = input.Request
				}
			}

			subAgentConfig := AgentConfig{
				Name:        subCfg.Name,
				Prompt:      prompt,
				Model:       subModel,
				Image:       subCfg.Image,
				Mounts:      parentConfig.Mounts,
				Storage:     parentConfig.Storage,
				RunID:       parentConfig.RunID,
				Namespace:   parentConfig.Namespace,
				PipelineID:  parentConfig.PipelineID,
				TriggeredBy: parentConfig.TriggeredBy,
				OnOutput:    parentConfig.OnOutput,
			}

			result, err := RunAgent(ctx, sandboxRunner, sm, pipelineID, subAgentConfig)
			if err != nil {
				return callAgentOutput{}, err
			}

			// Persist to a nested storage path so the UI tree renders the
			// sub-agent's result indented under the parent agent step.
			if subCfg.StorageKeyPrefix != "" && parentConfig.Storage != nil {
				storageKey := subCfg.StorageKeyPrefix + "/sub-agents/" + subCfg.Name + "/run"
				_ = parentConfig.Storage.Set(ctx, storageKey, map[string]any{
					"status":    result.Status,
					"stdout":    result.Text,
					"usage":     result.Usage,
					"audit_log": result.AuditLog,
				})
			}

			return callAgentOutput{
				Result: result.Text,
				Status: result.Status,
			}, nil
		},
	)
}
