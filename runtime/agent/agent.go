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

	agentmodel "github.com/jtarchie/pocketci/runtime/agent/model"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
)

// RunAgent executes an LLM agent with tools backed by a sandbox container.
// It writes a result.json to outputVolumePath when the agent finishes.
func RunAgent(
	ctx context.Context,
	sandboxRunner pipelinerunner.Runner,
	sm secrets.Manager,
	pipelineID string,
	config AgentConfig,
) (*AgentResult, error) {
	provider, modelName := agentmodel.SplitModel(config.Model)

	// Resolve API key: secrets (pipeline → global) then env var fallback.
	apiKey := agentmodel.ResolveSecret(ctx, sm, pipelineID, "agent/"+provider)
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
	llmModel, err := agentmodel.Resolve(provider, modelName, apiKey, config.LLM, config.Thinking)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	// Build the system instruction.
	maxTurns, maxTotalTokens := EffectiveLimits(config.Limits)
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

	// Build the full tool list, including any sub-agent tools.
	tools := []adktool.Tool{runScript, readFileTool, listTasksTool, getTaskResultTool}

	for _, subCfg := range config.SubAgents {
		subTool, subErr := buildSubAgentTool(ctx, sandbox, sandboxRunner, sm, pipelineID, subCfg, config)
		if subErr != nil {
			return nil, fmt.Errorf("agent: sub-agent %q: %w", subCfg.Name, subErr)
		}

		tools = append(tools, subTool)
	}

	// Create the ADK agent.
	genCfg := agentmodel.BuildGenerateContentConfig(provider, config.LLM, config.Thinking, config.Safety)

	myAgent, err := llmagent.New(llmagent.Config{
		Name:                  config.Name,
		Model:                 llmModel,
		Description:           "An agent running in a CI/CD system with access to a containerized environment.",
		Instruction:           instruction,
		Tools:                 tools,
		GenerateContentConfig: genCfg,
		// OutputSchema is NOT passed to ADK. Setting it causes the ADK to add
		// ResponseMIMEType="application/json" which forces the model to emit
		// JSON immediately, preventing it from making tool calls first. Instead
		// the schema is appended to the system instruction by
		// buildSystemInstruction and enforced by the validation follow-up.
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
		guard := contextguard.New(agentmodel.SimpleRegistry{})

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

	AppendAuditEvent(&auditEvents, AuditEvent{
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
	var textBuilder strings.Builder   // ALL text — streaming + audit log
	var resultBuilder strings.Builder // isFinal text only — result.json + AgentResult
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
				AppendAuditEvent(&auditEvents, AuditEvent{
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

				AppendAuditEvent(&auditEvents, AuditEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Author:    "system",
					Type:      "limit_warning",
					Text:      warningMsg,
				}, config.OnAuditEvent)

				warningInjected = true
			}

			// Check hard turn limit.
			if turnCount >= maxTurns {
				AppendAuditEvent(&auditEvents, AuditEvent{
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

		processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, eventUsage)
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
				"text":   resultBuilder.String(),
				"status": "success",
			}

			passed, evalErr := evalValidation(config.Validation.Expr, env)
			if evalErr != nil {
				AppendAuditEvent(&auditEvents, AuditEvent{
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
			needsFollowUp = resultBuilder.Len() == 0
		}

		if needsFollowUp {
			AppendAuditEvent(&auditEvents, AuditEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Author:    "system",
				Type:      "validation_followup",
				Text:      followUpText,
			}, config.OnAuditEvent)

			// Reset resultBuilder so the invalid output is not prepended to the retry.
			resultBuilder.Reset()

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

				processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, nil)
			}

			if runErr != nil {
				return nil, fmt.Errorf("agent: follow-up run failed: %w", runErr)
			}
		}
	}

	finalText := resultBuilder.String()
	status := "success"

	if limitExceeded {
		status = "limit_exceeded"
	}

	// Write result.json to the output path inside the sandbox if configured.
	outputMountPath := ResolveOutputMountPath(config)
	if outputMountPath != "" {
		resultData := map[string]string{"status": status, "text": finalText}
		data, err := json.Marshal(resultData)
		if err != nil {
			return nil, fmt.Errorf("agent: marshal output result: %w", err)
		}

		var execInput pipelinerunner.ExecInput
		execInput.Command.Path = "sh"
		execInput.Command.Args = []string{"-c", ResultJsonWriteCmd(outputMountPath, data)}

		execResult, execErr := sandbox.Exec(execInput)
		if execErr != nil {
			return nil, fmt.Errorf("agent: write result.json: %w", execErr)
		}

		if execResult == nil {
			return nil, errors.New("agent: write result.json: empty exec result")
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
