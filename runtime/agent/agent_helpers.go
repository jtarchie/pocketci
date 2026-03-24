package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/achetronic/adk-utils-go/plugin/contextguard"

	agentmodel "github.com/jtarchie/pocketci/runtime/agent/model"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
)

// buildSandboxTools creates the base set of sandbox-backed and storage tools
// shared by both parent agents and shared-container sub-agents.
func buildSandboxTools(
	ctx context.Context,
	sandbox *pipelinerunner.SandboxHandle,
	config AgentConfig,
) ([]adktool.Tool, error) {
	toolTimeout, scriptTimeout := EffectiveToolTimeouts(config.ToolTimeout)

	runScript, err := newRunScriptTool(sandbox, config.OnOutput, scriptTimeout) //nolint:contextcheck // ctx flows via adktool.Context at execution time
	if err != nil {
		return nil, fmt.Errorf("failed to create run_script tool: %w", err)
	}

	readFileTool, err := newReadFileTool(sandbox, config.OnOutput, toolTimeout) //nolint:contextcheck // ctx flows via adktool.Context at execution time
	if err != nil {
		return nil, fmt.Errorf("failed to create read_file tool: %w", err)
	}

	grepTool, err := newGrepTool(sandbox, config.OnOutput, toolTimeout) //nolint:contextcheck // ctx flows via adktool.Context at execution time
	if err != nil {
		return nil, fmt.Errorf("failed to create grep tool: %w", err)
	}

	globTool, err := newGlobTool(sandbox, config.OnOutput, toolTimeout) //nolint:contextcheck // ctx flows via adktool.Context at execution time
	if err != nil {
		return nil, fmt.Errorf("failed to create glob tool: %w", err)
	}

	writeFileTool, err := newWriteFileTool(sandbox, config.OnOutput, toolTimeout) //nolint:contextcheck // ctx flows via adktool.Context at execution time
	if err != nil {
		return nil, fmt.Errorf("failed to create write_file tool: %w", err)
	}

	listTasksTool, err := newListTasksTool(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create list_tasks tool: %w", err)
	}

	getTaskResultTool, err := newGetTaskResultTool(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create get_task_result tool: %w", err)
	}

	return []adktool.Tool{runScript, readFileTool, grepTool, globTool, writeFileTool, listTasksTool, getTaskResultTool}, nil
}

// buildAgentTools creates all the tools available to the agent, including
// sandbox tools, storage tools, and sub-agent tools.
func buildAgentTools(
	ctx context.Context,
	sandbox *pipelinerunner.SandboxHandle,
	sandboxRunner pipelinerunner.Runner,
	sm secrets.Manager,
	pipelineID string,
	config AgentConfig,
) ([]adktool.Tool, error) {
	tools, err := buildSandboxTools(ctx, sandbox, config)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	toolTimeout, _ := EffectiveToolTimeouts(config.ToolTimeout)

	for _, toolDef := range config.Tools {
		var tool adktool.Tool
		var toolErr error

		if toolDef.IsTask {
			tool, toolErr = newTaskTool(sandbox, toolDef, config, toolTimeout) //nolint:contextcheck // ctx flows via adktool.Context at execution time
		} else {
			tool, toolErr = buildSubAgentTool(ctx, sandbox, sandboxRunner, sm, pipelineID, toolDef, config)
		}

		if toolErr != nil {
			return nil, fmt.Errorf("agent: tool %q: %w", toolDef.Name, toolErr)
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

// setupAgentSession creates the in-memory session and runner, optionally
// installing the context guard plugin.
func setupAgentSession(
	ctx context.Context,
	myAgent agent.Agent,
	config AgentConfig,
	llmModel adkmodel.LLM,
) (session.Service, *session.CreateResponse, *runner.Runner, error) {
	sessionService := session.InMemoryService()

	sessResp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "ci-agent",
		UserID:  "pipeline",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("agent: failed to create session: %w", err)
	}

	runnr, err := runner.New(runner.Config{
		AppName:        "ci-agent",
		Agent:          myAgent,
		SessionService: sessionService,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("agent: failed to create runner: %w", err)
	}

	if config.ContextGuard != nil {
		guard := contextguard.New(agentmodel.SimpleRegistry{})

		opts, optionsErr := resolveContextGuardOptions(config.ContextGuard)
		if optionsErr != nil {
			return nil, nil, nil, fmt.Errorf("agent: %w", optionsErr)
		}

		guard.Add(config.Name, llmModel, opts...)

		pluginCfg := guard.PluginConfig()
		runnr, err = runner.New(runner.Config{
			AppName:        "ci-agent",
			Agent:          myAgent,
			SessionService: sessionService,
			PluginConfig:   pluginCfg,
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("agent: failed to create runner with context guard: %w", err)
		}
	}

	return sessionService, sessResp, runnr, nil
}

// seedSessionWithPrompt adds the user prompt to the session and audit log.
func seedSessionWithPrompt(
	ctx context.Context,
	sessionService session.Service,
	sess session.Session,
	config AgentConfig,
	now time.Time,
	auditEvents *[]AuditEvent,
) {
	userInvID := uuid.NewString()
	userEvent := session.NewEvent(userInvID)
	userEvent.Author = "user"
	userEvent.LLMResponse = adkmodel.LLMResponse{
		Content: genai.NewContentFromText(config.Prompt, genai.RoleUser),
	}

	_ = sessionService.AppendEvent(ctx, sess, userEvent)

	AppendAuditEvent(auditEvents, AuditEvent{
		Timestamp: now.Format(time.RFC3339),
		Author:    "user",
		Type:      "user_message",
		Text:      config.Prompt,
	}, config.OnAuditEvent)
}

// agentLoopResult holds the accumulated state from the main agent event loop.
type agentLoopResult struct {
	textBuilder   strings.Builder
	resultBuilder strings.Builder
	usage         AgentUsage
	limitExceeded bool
}

// executeAgentLoop runs the main agent event loop, tracking usage and limits.
func executeAgentLoop(
	ctx context.Context,
	runnr *runner.Runner,
	sessionID string,
	config AgentConfig,
	maxTurns int,
	maxTotalTokens int32,
	auditEvents *[]AuditEvent,
) (*agentLoopResult, error) {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	var result agentLoopResult
	var turnCount int
	warningInjected := false

	for event, err := range runnr.Run(runCtx, "pipeline", sessionID, nil, agent.RunConfig{}) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				return nil, fmt.Errorf("agent: run failed: %w", err)
			}

			break
		}

		var eventUsage *AuditUsage
		if event.UsageMetadata != nil {
			accumulateUsage(&result.usage, event.UsageMetadata, config.OnUsage)
			turnCount++
			eventUsage = &AuditUsage{
				PromptTokens:     event.UsageMetadata.PromptTokenCount,
				CompletionTokens: event.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      event.UsageMetadata.TotalTokenCount,
			}

			if exceeded, warned := checkAgentLimits(turnCount, maxTurns, maxTotalTokens, &result.usage, warningInjected, auditEvents, config); exceeded {
				result.limitExceeded = true
				cancelRun()

				break
			} else if warned {
				warningInjected = true
			}
		}

		processEventParts(event, &result.usage, auditEvents, &result.textBuilder, &result.resultBuilder, config, eventUsage)
	}

	return &result, nil
}

// checkAgentLimits checks token and turn limits, emitting warnings and
// returning whether the limit was exceeded and whether a warning was injected.
func checkAgentLimits(
	turnCount, maxTurns int,
	maxTotalTokens int32,
	usage *AgentUsage,
	warningInjected bool,
	auditEvents *[]AuditEvent,
	config AgentConfig,
) (exceeded, warned bool) {
	if maxTotalTokens > 0 && usage.TotalTokens >= maxTotalTokens {
		AppendAuditEvent(auditEvents, AuditEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Type:      "limit_warning",
			Text:      fmt.Sprintf("Total token budget exhausted (%d/%d tokens used). Stopping agent.", usage.TotalTokens, maxTotalTokens),
		}, config.OnAuditEvent)

		return true, warningInjected
	}

	if !warningInjected && turnCount == maxTurns-limitWarningTurnsBefore {
		AppendAuditEvent(auditEvents, AuditEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Type:      "limit_warning",
			Text: fmt.Sprintf(
				"You are approaching your turn limit (%d/%d turns used). "+
					"Please wrap up your current task and provide a final response within the next %d turn(s).",
				turnCount, maxTurns, limitWarningTurnsBefore,
			),
		}, config.OnAuditEvent)

		warned = true
	}

	if turnCount >= maxTurns {
		AppendAuditEvent(auditEvents, AuditEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Type:      "limit_warning",
			Text:      fmt.Sprintf("Turn limit reached (%d/%d). Stopping agent.", turnCount, maxTurns),
		}, config.OnAuditEvent)

		return true, warned
	}

	return false, warned
}

// buildAgentResult constructs the final AgentResult, optionally writing
// result.json to the sandbox output path.
func buildAgentResult(
	ctx context.Context,
	sandbox *pipelinerunner.SandboxHandle,
	config AgentConfig,
	loopResult *agentLoopResult,
	auditEvents []AuditEvent,
) (*AgentResult, error) {
	finalText := loopResult.resultBuilder.String()
	status := "success"

	if loopResult.limitExceeded {
		status = "limit_exceeded"
	}

	outputMountPath := ResolveOutputMountPath(config)
	if outputMountPath != "" {
		if err := writeResultJSON(ctx, sandbox, outputMountPath, status, finalText); err != nil {
			return nil, err
		}
	}

	return &AgentResult{
		Text:     finalText,
		Status:   status,
		Usage:    loopResult.usage,
		AuditLog: auditEvents,
	}, nil
}

// writeResultJSON writes the agent result to the sandbox output path.
func writeResultJSON(ctx context.Context, sandbox *pipelinerunner.SandboxHandle, outputMountPath, status, finalText string) error {
	resultData := map[string]string{"status": status, "text": finalText}

	data, err := json.Marshal(resultData)
	if err != nil {
		return fmt.Errorf("agent: marshal output result: %w", err)
	}

	var execInput pipelinerunner.ExecInput
	execInput.Command.Path = "sh"
	execInput.Command.Args = []string{"-c", ResultJsonWriteCmd(outputMountPath, data)}

	execResult, execErr := sandbox.Exec(ctx, execInput)
	if execErr != nil {
		return fmt.Errorf("agent: write result.json: %w", execErr)
	}

	if execResult == nil {
		return errors.New("agent: write result.json: empty exec result")
	}

	if execResult.Code != 0 {
		return fmt.Errorf("agent: write result.json failed with exit code %d: %s", execResult.Code, execResult.Stderr)
	}

	return nil
}
