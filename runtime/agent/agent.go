package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/runner"
	"google.golang.org/genai"

	agentmodel "github.com/jtarchie/pocketci/runtime/agent/model"
	"github.com/jtarchie/pocketci/runtime/agent/schema"
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

	llmModel, err := agentmodel.Resolve(provider, modelName, apiKey, config.LLM, config.Thinking)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}

	tools, err := buildAgentTools(ctx, sandbox, sandboxRunner, sm, pipelineID, config)
	if err != nil {
		return nil, err
	}

	maxTurns, maxTotalTokens := EffectiveLimits(config.Limits)

	var expandedSchema *genai.Schema
	if config.OutputSchema != nil {
		expandedSchema = schema.ExpandOutputSchema(config.OutputSchema)
	}

	instruction := BuildSystemInstruction(config, maxTurns, expandedSchema)
	genCfg := agentmodel.BuildGenerateContentConfig(provider, config.LLM, config.Thinking, config.Safety)

	myAgent, err := llmagent.New(llmagent.Config{
		Name:                  config.Name,
		Model:                 llmModel,
		Description:           "An agent running in a CI/CD system with access to a containerized environment.",
		Instruction:           instruction,
		Tools:                 tools,
		GenerateContentConfig: genCfg,
	})
	if err != nil {
		return nil, fmt.Errorf("agent: failed to create agent: %w", err)
	}

	sessionService, sessResp, runnr, err := setupAgentSession(ctx, myAgent, config, llmModel)
	if err != nil {
		return nil, err
	}

	var auditEvents []AuditEvent
	now := time.Now().UTC()

	seedSessionWithPrompt(ctx, sessionService, sessResp.Session, config, now, &auditEvents)
	injectListTasksContext(ctx, sessionService, sessResp.Session, config, now, &auditEvents)
	injectTaskContexts(ctx, sessionService, sessResp.Session, config, now, &auditEvents)
	injectFileContexts(ctx, sandbox, sessionService, sessResp.Session, config, now, &auditEvents)

	result, err := executeAgentLoop(ctx, runnr, sessResp.Session.ID(), config, maxTurns, maxTotalTokens, &auditEvents)
	if err != nil {
		return nil, err
	}

	if !result.limitExceeded {
		if runErr := runValidationFollowUp(
			ctx, runnr, sessResp.Session.ID(), config, expandedSchema,
			&result.resultBuilder, &result.textBuilder, &result.usage, &auditEvents,
		); runErr != nil {
			return nil, fmt.Errorf("agent: follow-up run failed: %w", runErr)
		}
	}

	return buildAgentResult(ctx, sandbox, config, result, auditEvents)
}

// runValidationFollowUp checks the agent's output and sends a follow-up turn
// if validation fails or no text was produced.
func runValidationFollowUp(
	runCtx context.Context,
	runnr *runner.Runner,
	sessionID string,
	config AgentConfig,
	expandedSchema *genai.Schema,
	resultBuilder, textBuilder *strings.Builder,
	usage *AgentUsage,
	auditEvents *[]AuditEvent,
) error {
	needsFollowUp, followUpText := evaluateValidation(config, expandedSchema, resultBuilder, auditEvents)
	if !needsFollowUp {
		return nil
	}

	AppendAuditEvent(auditEvents, AuditEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Author:    "system",
		Type:      "validation_followup",
		Text:      followUpText,
	}, config.OnAuditEvent)

	resultBuilder.Reset()
	followUpMsg := genai.NewContentFromText(followUpText, genai.RoleUser)

	for event, err := range runnr.Run(runCtx, "pipeline", sessionID, followUpMsg, agent.RunConfig{}) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				return err
			}

			break
		}

		if event.UsageMetadata != nil {
			accumulateUsage(usage, event.UsageMetadata, config.OnUsage)
		}

		if event.Content == nil {
			continue
		}

		processEventParts(event, usage, auditEvents, textBuilder, resultBuilder, config, nil)
	}

	return nil
}

// evaluateValidation checks whether the agent output passes validation.
func evaluateValidation(config AgentConfig, expandedSchema *genai.Schema, resultBuilder *strings.Builder, auditEvents *[]AuditEvent) (bool, string) {
	followUpText := "You have not provided a final text response yet. " +
		"Please produce your complete response now based on the information you have gathered."

	if resultBuilder.Len() == 0 {
		return true, followUpText
	}

	text := resultBuilder.String()

	// Check output schema conformance first (structural validation).
	if expandedSchema != nil {
		if err := schema.ValidateJSON(text, expandedSchema); err != nil {
			schemaFollowUp := fmt.Sprintf(
				"Your response is not valid JSON conforming to the required schema. Error: %s. "+
					"Please output ONLY a valid JSON object matching the schema, with no surrounding text.",
				err.Error(),
			)

			AppendAuditEvent(auditEvents, AuditEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Author:    "system",
				Type:      "schema_validation_error",
				Text:      schemaFollowUp,
			}, config.OnAuditEvent)

			return true, schemaFollowUp
		}
	}

	// Then check expr-based validation (semantic validation).
	if config.Validation == nil || config.Validation.Expr == "" {
		return false, ""
	}

	env := map[string]any{
		"text":   text,
		"status": "success",
	}

	passed, evalErr := evalValidation(config.Validation.Expr, env)
	if evalErr != nil {
		AppendAuditEvent(auditEvents, AuditEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Type:      "validation_error",
			Text:      fmt.Sprintf("Validation expression error: %v", evalErr),
		}, config.OnAuditEvent)

		if config.Validation.Prompt != "" {
			return true, config.Validation.Prompt
		}

		return true, followUpText
	}

	if !passed && config.Validation.Prompt != "" {
		return true, config.Validation.Prompt
	}

	return !passed, followUpText
}
