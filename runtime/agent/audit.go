package agent

import (
	"strings"
	"time"

	"google.golang.org/adk/session"
	"google.golang.org/genai"

	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
)

// EmitUsageSnapshot calls the usage callback if non-nil.
func EmitUsageSnapshot(onUsage func(AgentUsage), usage AgentUsage) {
	if onUsage != nil {
		onUsage(usage)
	}
}

// AppendAuditEvent appends an event and calls the callback if non-nil.
func AppendAuditEvent(auditEvents *[]AuditEvent, event AuditEvent, onAuditEvent func(AuditEvent)) {
	*auditEvents = append(*auditEvents, event)
	if onAuditEvent != nil {
		onAuditEvent(event)
	}
}

// accumulateUsage adds event-level token counts to the running total and emits a snapshot.
func accumulateUsage(usage *AgentUsage, meta *genai.GenerateContentResponseUsageMetadata, onUsage func(AgentUsage)) {
	usage.PromptTokens += meta.PromptTokenCount
	usage.CompletionTokens += meta.CandidatesTokenCount
	usage.TotalTokens += meta.TotalTokenCount
	usage.LLMRequests++
	EmitUsageSnapshot(onUsage, *usage)
}

// processTextOutput writes text to the builder, calls the output callback, and appends an audit event.
func processTextOutput(
	text string,
	textBuilder *strings.Builder,
	onOutput pipelinerunner.OutputCallback,
	auditEvents *[]AuditEvent,
	event AuditEvent,
	onAuditEvent func(AuditEvent),
) {
	textBuilder.WriteString(text)
	if onOutput != nil {
		onOutput("stdout", text)
	}
	AppendAuditEvent(auditEvents, event, onAuditEvent)
}

// processEventParts handles FunctionCall, FunctionResponse, and Text parts
// from an ADK event. Used by the main run loop, validation follow-up, and
// sub-agent event processing. Callbacks in config are nil-safe.
func processEventParts(
	event *session.Event,
	usage *AgentUsage,
	auditEvents *[]AuditEvent,
	textBuilder *strings.Builder,
	resultBuilder *strings.Builder,
	config AgentConfig,
	eventUsage *AuditUsage,
) {
	if event.Content == nil {
		return
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	if !event.Timestamp.IsZero() {
		ts = event.Timestamp.UTC().Format(time.RFC3339)
	}

	isFinal := event.IsFinalResponse()
	usageAttached := false

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
			}, config.OnAuditEvent)
			EmitUsageSnapshot(config.OnUsage, *usage)
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

		processTextOutput(part.Text, textBuilder, config.OnOutput, auditEvents, ae, config.OnAuditEvent)

		if isFinal {
			resultBuilder.WriteString(part.Text)
		}
	}
}
