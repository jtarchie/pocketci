package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/adk/session"
	"google.golang.org/genai"

	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
)

// ResultJsonWriteCmd builds a shell command that creates mountName/ and writes
// data to mountName/result.json without relying on stdin.
// The data bytes are embedded directly in the command using POSIX single-quote
// escaping so the command is safe at any shell-nesting depth (e.g. Fly's
// nested sh -c chain where stdin is not piped through to the inner process).
func ResultJsonWriteCmd(mountName string, data []byte) string {
	escaped := "'" + strings.ReplaceAll(string(data), "'", `'\''`) + "'"
	return fmt.Sprintf("mkdir -p %s && printf '%%s' %s > %s/result.json",
		strconv.Quote(mountName), escaped, strconv.Quote(mountName))
}

// ResolveOutputMountPath maps host-path-like values back to mount names used in sandbox.
func ResolveOutputMountPath(config AgentConfig) string {
	value := strings.TrimSpace(config.OutputVolumePath)
	if value == "" {
		return ""
	}

	if _, ok := config.Mounts[value]; ok {
		return value
	}

	for mountPath, volume := range config.Mounts {
		if volume.Path == value || volume.Name == value {
			return mountPath
		}
	}

	return value
}

// buildSystemInstruction constructs the system instruction describing the
// agent's environment. The user's task prompt is sent separately as a user
// message so the model receives it in the correct context-window slot.
func buildSystemInstruction(config AgentConfig, maxTurns int) string {
	var b strings.Builder

	b.WriteString("You are operating inside a CI/CD pipeline run.\n")
	b.WriteString("\n")

	if config.Image != "" {
		fmt.Fprintf(&b, "Container image: %s\n", config.Image)
	}

	if config.RunID != "" {
		fmt.Fprintf(&b, "Pipeline run ID: %s\n", config.RunID)
	}

	if config.PipelineID != "" {
		fmt.Fprintf(&b, "Pipeline ID: %s\n", config.PipelineID)
	}

	if config.TriggeredBy != "" {
		fmt.Fprintf(&b, "Triggered by: %s\n", config.TriggeredBy)
	}

	if len(config.Mounts) > 0 {
		b.WriteString("\nAvailable volumes (accessible as relative paths from the working directory):\n")

		for name := range config.Mounts {
			fmt.Fprintf(&b, "  - %s/\n", name)
		}
	}

	b.WriteString("\nTools available:\n")
	b.WriteString("  - run_script: run a multi-line /bin/sh script\n")
	b.WriteString("  - read_file: read a volume file by path without a shell (e.g. \"diff/pr.diff\")\n")
	b.WriteString("  - list_tasks: list all tasks in the current run with their statuses (pre-fetched at start)\n")
	b.WriteString("  - get_task_result: retrieve stdout, stderr, and exit code for a specific task by name\n")

	b.WriteString("\nEfficiency rules:\n")
	b.WriteString("  - Each tool call costs one full LLM round-trip. Minimise calls.\n")
	b.WriteString("  - When you need multiple sequential shell steps, combine them into ONE run_script call (use 'set -e' so failures abort early).\n")
	b.WriteString("  - Only use separate tool calls when you need to branch on intermediate output.\n")
	b.WriteString("  - If context already contains the data you need (injected task results, volume file contents), do NOT re-read it with a tool call.\n")

	fmt.Fprintf(&b, "\nYou have a budget of %d turns. Use run_script to combine steps and finish well within this limit.\n", maxTurns)

	if config.OutputSchema != nil {
		schema := ExpandOutputSchema(config.OutputSchema)
		if schema != nil {
			schemaJSON, err := json.Marshal(schema)
			if err == nil {
				b.WriteString("\nOutput format:\n")
				b.WriteString("Your FINAL response must be valid JSON conforming to this schema:\n")
				fmt.Fprintf(&b, "%s\n", schemaJSON)
				b.WriteString("Do not include any text outside the JSON object in your final response.\n")
			}
		}
	}

	return b.String()
}

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
// from an ADK event. Used by both the main run loop and the validation
// follow-up loop so tool calls are always tracked.
func processEventParts(
	event *session.Event,
	usage *AgentUsage,
	auditEvents *[]AuditEvent,
	textBuilder *strings.Builder,
	resultBuilder *strings.Builder,
	config AgentConfig,
	eventUsage *AuditUsage,
) {
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
