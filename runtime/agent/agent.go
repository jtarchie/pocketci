package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/achetronic/adk-utils-go/plugin/contextguard"

	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

// AgentLLMConfig controls LLM generation parameters.
type AgentLLMConfig struct {
	Temperature *float32 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	MaxTokens   int32    `yaml:"max_tokens,omitempty"   json:"max_tokens,omitempty"`
}

// AgentThinkingConfig enables extended thinking for supported models.
// Budget sets the maximum thinking tokens (>= 1024).
// Level is Gemini-specific: LOW | MEDIUM | HIGH | MINIMAL.
type AgentThinkingConfig struct {
	Budget int32  `yaml:"budget"          json:"budget"`
	Level  string `yaml:"level,omitempty" json:"level,omitempty"`
}

// AgentConfig is the configuration passed from JavaScript to runtime.agent().
type AgentConfig struct {
	Name             string                                 `json:"name"`
	Prompt           string                                 `json:"prompt"`
	Model            string                                 `json:"model"`
	Image            string                                 `json:"image"`
	Mounts           map[string]pipelinerunner.VolumeResult `json:"mounts"`
	OutputVolumePath string                                 `json:"outputVolumePath"`
	LLM              *AgentLLMConfig                        `json:"llm,omitempty"`
	Thinking         *AgentThinkingConfig                   `json:"thinking,omitempty"`
	Safety           map[string]string                      `json:"safety,omitempty"`
	ContextGuard     *AgentContextGuardConfig               `json:"context_guard,omitempty"`
	Limits           *AgentLimitsConfig                     `json:"limits,omitempty"`
	Context          *AgentContext                          `json:"context,omitempty"`
	Validation       *AgentValidationConfig                 `json:"validation,omitempty"`
	// OnOutput is called with streaming chunks. Not serialised from JS.
	OnOutput pipelinerunner.OutputCallback `json:"-"`
	// OnAuditEvent is called every time an audit event is appended.
	OnAuditEvent func(AuditEvent) `json:"-"`
	// OnUsage is called whenever cumulative usage changes.
	OnUsage func(AgentUsage) `json:"-"`
	// Internal fields populated by Runtime.Agent() — not exposed to JS.
	Storage     storage.Driver `json:"-"`
	Namespace   string         `json:"-"`
	RunID       string         `json:"-"`
	PipelineID  string         `json:"-"`
	TriggeredBy string         `json:"-"`
}

// AgentResult is returned to JavaScript after the agent completes.
type AgentResult struct {
	Text     string       `json:"text"`
	Status   string       `json:"status"` // "success", "failure", or "limit_exceeded"
	Usage    AgentUsage   `json:"usage"`
	AuditLog []AuditEvent `json:"auditLog"`
}

// AgentUsage tracks cumulative token counts and request stats.
type AgentUsage struct {
	PromptTokens     int32 `json:"promptTokens"`
	CompletionTokens int32 `json:"completionTokens"`
	TotalTokens      int32 `json:"totalTokens"`
	LLMRequests      int   `json:"llmRequests"`
	ToolCallCount    int   `json:"toolCallCount"`
}

// AuditUsage holds per-event token counts reported by the LLM.
type AuditUsage struct {
	PromptTokens     int32 `json:"promptTokens"`
	CompletionTokens int32 `json:"completionTokens"`
	TotalTokens      int32 `json:"totalTokens"`
}

// AuditEvent is a single entry in the agent audit log.
// Type values:
//   - "pre_context"   — synthetic tool call injected before the first turn
//   - "user_message"  — the initial user prompt
//   - "tool_call"     — a function call made by the model
//   - "tool_response" — the result returned to the model
//   - "model_text"    — an intermediate model text chunk
//   - "model_final"   — the final model response
type AuditEvent struct {
	Timestamp    string         `json:"timestamp,omitempty"`
	InvocationID string         `json:"invocationId,omitempty"`
	Author       string         `json:"author,omitempty"`
	Type         string         `json:"type"`
	Text         string         `json:"text,omitempty"`
	ToolName     string         `json:"toolName,omitempty"`
	ToolCallID   string         `json:"toolCallId,omitempty"`
	ToolArgs     map[string]any `json:"toolArgs,omitempty"`
	ToolResult   map[string]any `json:"toolResult,omitempty"`
	Usage        *AuditUsage    `json:"usage,omitempty"`
}

// RunAgent executes an LLM agent with tools backed by a sandbox container.
// It writes a result.json to outputVolumePath when the agent finishes.
func RunAgent(
	ctx context.Context,
	sandboxRunner pipelinerunner.Runner,
	sm secrets.Manager,
	pipelineID string,
	config AgentConfig,
) (*AgentResult, error) {
	provider, modelName := splitModel(config.Model)

	// Resolve API key: secrets (pipeline → global) then env var fallback.
	apiKey := resolveSecret(ctx, sm, pipelineID, "agent/"+provider)
	if apiKey == "" {
		envKey := strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
		apiKey = os.Getenv(envKey)
	}

	// Start the sandbox container.
	sandbox, err := sandboxRunner.StartSandbox(pipelinerunner.SandboxInput{
		Image:  config.Image,
		Name:   config.Name,
		Mounts: config.Mounts,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: failed to start sandbox: %w", err)
	}

	defer func() { _ = sandbox.Close() }()

	// Build sandbox tools.
	runScript, err := newRunScriptTool(sandbox, config.OnOutput)
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create run_script tool: %w", err)
	}

	readFileTool, err := newReadFileTool(sandbox, config.OnOutput)
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create read_file tool: %w", err)
	}

	// Resolve the LLM model.
	llmModel, err := resolveModel(provider, modelName, apiKey, config.LLM, config.Thinking)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	// Build the system instruction.
	maxTurns, maxTotalTokens := effectiveLimits(config.Limits)
	instruction := buildSystemInstruction(config, maxTurns)

	// Build storage-backed tools.
	listTasksTool, err := newListTasksTool(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create list_tasks tool: %w", err)
	}

	getTaskResultTool, err := newGetTaskResultTool(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create get_task_result tool: %w", err)
	}

	// Create the ADK agent.
	genCfg := buildGenerateContentConfig(provider, config.LLM, config.Thinking, config.Safety)

	myAgent, err := llmagent.New(llmagent.Config{
		Name:                  config.Name,
		Model:                 llmModel,
		Description:           "An agent running in a CI/CD system with access to a containerized environment.",
		Instruction:           instruction,
		Tools:                 []adktool.Tool{runScript, readFileTool, listTasksTool, getTaskResultTool},
		GenerateContentConfig: genCfg,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create agent: %w", err)
	}

	// Set up an in-memory session.
	sessionService := session.InMemoryService()

	sessResp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: "ci-agent",
		UserID:  "pipeline",
	})
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create session: %w", err)
	}

	runnr, err := runner.New(runner.Config{
		AppName:        "ci-agent",
		Agent:          myAgent,
		SessionService: sessionService,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create runner: %w", err)
	}

	// Wire context guard plugin when requested.
	if config.ContextGuard != nil {
		guard := contextguard.New(simpleRegistry{})

		opts, optionsErr := resolveContextGuardOptions(config.ContextGuard)
		if optionsErr != nil {
			return nil, fmt.Errorf("agent: %w", optionsErr)
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
			return nil, fmt.Errorf("agent: failed to create runner with context guard: %w", err)
		}
	}

	// Initialise audit log and base timestamp for pre-context entries.
	var auditEvents []AuditEvent
	now := time.Now().UTC()

	// Add the user message to the session first so that pre-context synthetic
	// tool calls appear AFTER it in conversation history — the LLM sees:
	//   1. User: "Review this PR..."
	//   2. Model: [calls get_task_result / read_file (synthetic)]
	//   3. User: [result]
	//   4. Model generates its real response
	// We then pass nil to runnr.Run() so it doesn't append a duplicate message.
	userInvID := uuid.NewString()
	userEvent := session.NewEvent(userInvID)
	userEvent.Author = "user"
	userEvent.LLMResponse = adkmodel.LLMResponse{
		Content: genai.NewContentFromText(config.Prompt, genai.RoleUser),
	}

	_ = sessionService.AppendEvent(ctx, sessResp.Session, userEvent)

	appendAuditEvent(&auditEvents, AuditEvent{
		Timestamp: now.Format(time.RFC3339),
		Author:    "user",
		Type:      "user_message",
		Text:      config.Prompt,
	}, config.OnAuditEvent)

	// Pre-inject context into the session.
	injectListTasksContext(ctx, sessionService, sessResp.Session, config, now, &auditEvents)
	injectTaskContexts(ctx, sessionService, sessResp.Session, config, now, &auditEvents)
	injectFileContexts(ctx, sandbox, sessionService, sessResp.Session, config, now, &auditEvents)

	// Run the agent. The user message was already appended to the session above
	// so we pass nil here — runnr.Run early-returns from appendMessageToSession
	// when msg is nil, avoiding a duplicate turn.
	var textBuilder strings.Builder
	var usage AgentUsage

	// Wrap context so we can cancel on hard limit.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	var turnCount int
	limitExceeded := false
	warningInjected := false

	var runErr error

	for event, err := range runnr.Run(runCtx, "pipeline", sessResp.Session.ID(), nil, agent.RunConfig{}) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				runErr = err
			}

			break
		}

		// Accumulate token usage from every LLM response.
		var eventUsage *AuditUsage
		if event.UsageMetadata != nil {
			accumulateUsage(&usage, event.UsageMetadata, config.OnUsage)
			turnCount++
			eventUsage = &AuditUsage{
				PromptTokens:     event.UsageMetadata.PromptTokenCount,
				CompletionTokens: event.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      event.UsageMetadata.TotalTokenCount,
			}

			// Check hard token limit.
			if maxTotalTokens > 0 && usage.TotalTokens >= maxTotalTokens {
				appendAuditEvent(&auditEvents, AuditEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Author:    "system",
					Type:      "limit_warning",
					Text:      fmt.Sprintf("Total token budget exhausted (%d/%d tokens used). Stopping agent.", usage.TotalTokens, maxTotalTokens),
				}, config.OnAuditEvent)

				limitExceeded = true
				cancelRun()

				break
			}

			// Inject a warning when approaching the turn limit.
			if !warningInjected && turnCount == maxTurns-limitWarningTurnsBefore {
				warningMsg := fmt.Sprintf(
					"You are approaching your turn limit (%d/%d turns used). "+
						"Please wrap up your current task and provide a final response within the next %d turn(s).",
					turnCount, maxTurns, limitWarningTurnsBefore,
				)

				appendAuditEvent(&auditEvents, AuditEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Author:    "system",
					Type:      "limit_warning",
					Text:      warningMsg,
				}, config.OnAuditEvent)

				warningInjected = true
			}

			// Check hard turn limit.
			if turnCount >= maxTurns {
				appendAuditEvent(&auditEvents, AuditEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Author:    "system",
					Type:      "limit_warning",
					Text:      fmt.Sprintf("Turn limit reached (%d/%d). Stopping agent.", turnCount, maxTurns),
				}, config.OnAuditEvent)

				limitExceeded = true
				cancelRun()

				break
			}
		}

		if event.Content == nil {
			continue
		}

		// Compute timestamp and finality for audit events.
		ts := now.Format(time.RFC3339)
		if !event.Timestamp.IsZero() {
			ts = event.Timestamp.UTC().Format(time.RFC3339)
		}

		isFinal := event.IsFinalResponse()
		usageAttached := false

		for _, part := range event.Content.Parts {
			// Track function calls (tool invocations by the model).
			if part.FunctionCall != nil {
				fc := part.FunctionCall
				usage.ToolCallCount++

				appendAuditEvent(&auditEvents, AuditEvent{
					Timestamp:    ts,
					InvocationID: event.InvocationID,
					Author:       event.Author,
					Type:         "tool_call",
					ToolName:     fc.Name,
					ToolCallID:   fc.ID,
					ToolArgs:     fc.Args,
				}, config.OnAuditEvent)
				emitUsageSnapshot(config.OnUsage, usage)
			}

			// Track function responses (tool results).
			if part.FunctionResponse != nil {
				fr := part.FunctionResponse

				appendAuditEvent(&auditEvents, AuditEvent{
					Timestamp:    ts,
					InvocationID: event.InvocationID,
					Author:       event.Author,
					Type:         "tool_response",
					ToolName:     fr.Name,
					ToolCallID:   fr.ID,
					ToolResult:   fr.Response,
				}, config.OnAuditEvent)
			}

			if part.Text == "" {
				continue
			}

			eventType := "model_text"
			if isFinal {
				eventType = "model_final"
			}

			ae := AuditEvent{
				Timestamp:    ts,
				InvocationID: event.InvocationID,
				Author:       event.Author,
				Type:         eventType,
				Text:         part.Text,
			}

			if !usageAttached {
				ae.Usage = eventUsage
				usageAttached = true
			}

			processTextOutput(part.Text, &textBuilder, config.OnOutput, &auditEvents, ae, config.OnAuditEvent)
		}
	}

	if runErr != nil {
		return nil, fmt.Errorf("agent: run failed: %w", runErr)
	}

	// Validate the agent's output. When a validation expression is configured,
	// evaluate it; otherwise fall back to checking that some text was produced.
	// If validation fails, send a follow-up turn asking the model to correct.
	if !limitExceeded {
		needsFollowUp := false
		followUpText := "You have not provided a final text response yet. " +
			"Please produce your complete response now based on the information you have gathered."

		if config.Validation != nil && config.Validation.Expr != "" {
			env := map[string]any{
				"text":   textBuilder.String(),
				"status": "success",
			}

			passed, evalErr := evalValidation(config.Validation.Expr, env)
			if evalErr != nil {
				appendAuditEvent(&auditEvents, AuditEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Author:    "system",
					Type:      "validation_error",
					Text:      fmt.Sprintf("Validation expression error: %v", evalErr),
				}, config.OnAuditEvent)

				needsFollowUp = true
			} else if !passed {
				needsFollowUp = true
			}

			if needsFollowUp && config.Validation.Prompt != "" {
				followUpText = config.Validation.Prompt
			}
		} else {
			// Default: follow up when the model produced no text at all.
			needsFollowUp = textBuilder.Len() == 0
		}

		if needsFollowUp {
			appendAuditEvent(&auditEvents, AuditEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Author:    "system",
				Type:      "validation_followup",
				Text:      followUpText,
			}, config.OnAuditEvent)

			followUpMsg := genai.NewContentFromText(followUpText, genai.RoleUser)

			for event, err := range runnr.Run(runCtx, "pipeline", sessResp.Session.ID(), followUpMsg, agent.RunConfig{}) {
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						runErr = err
					}

					break
				}

				if event.UsageMetadata != nil {
					accumulateUsage(&usage, event.UsageMetadata, config.OnUsage)
				}

				if event.Content == nil {
					continue
				}

				for _, part := range event.Content.Parts {
					if part.Text == "" {
						continue
					}

					processTextOutput(part.Text, &textBuilder, config.OnOutput, &auditEvents, AuditEvent{
						Timestamp: time.Now().UTC().Format(time.RFC3339),
						Author:    event.Author,
						Type:      "model_final",
						Text:      part.Text,
					}, config.OnAuditEvent)
				}
			}

			if runErr != nil {
				return nil, fmt.Errorf("agent: follow-up run failed: %w", runErr)
			}
		}
	}

	finalText := textBuilder.String()
	status := "success"

	if limitExceeded {
		status = "limit_exceeded"
	}

	// Write result.json to the output path inside the sandbox if configured.
	outputMountPath := resolveOutputMountPath(config)
	if outputMountPath != "" {
		resultData := map[string]string{"status": status, "text": finalText}
		data, err := json.Marshal(resultData)
		if err != nil {
			return nil, fmt.Errorf("agent: marshal output result: %w", err)
		}

		var execInput pipelinerunner.ExecInput
		execInput.Command.Path = "sh"
		execInput.Command.Args = []string{"-c", resultJsonWriteCmd(outputMountPath, data)}

		execResult, execErr := sandbox.Exec(execInput)
		if execErr != nil {
			return nil, fmt.Errorf("agent: write result.json: %w", execErr)
		}

		if execResult == nil {
			return nil, fmt.Errorf("agent: write result.json: empty exec result")
		}

		if execResult.Code != 0 {
			return nil, fmt.Errorf("agent: write result.json failed with exit code %d: %s", execResult.Code, execResult.Stderr)
		}
	}

	return &AgentResult{
		Text:     finalText,
		Status:   status,
		Usage:    usage,
		AuditLog: auditEvents,
	}, nil
}
