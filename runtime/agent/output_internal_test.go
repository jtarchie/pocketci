package agent

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// makeFunctionCallEvent returns an event whose only part is a FunctionCall.
// IsFinalResponse() returns false for such events.
func makeFunctionCallEvent(invID, author, toolName, callID string, args map[string]any) *session.Event {
	event := session.NewEvent(invID)
	event.Author = author
	event.LLMResponse.Content = &genai.Content{
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: toolName, ID: callID, Args: args}},
		},
	}
	return event
}

// makeFunctionResponseEvent returns an event whose only part is a FunctionResponse.
// IsFinalResponse() returns false for such events.
func makeFunctionResponseEvent(invID, author, toolName, callID string, resp map[string]any) *session.Event {
	event := session.NewEvent(invID)
	event.Author = author
	event.LLMResponse.Content = &genai.Content{
		Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: toolName, ID: callID, Response: resp}},
		},
	}
	return event
}

// makeTextEvent returns a text-only event. Since it has no FunctionCalls,
// IsFinalResponse() returns true.
func makeTextEvent(invID, author, text string) *session.Event {
	event := session.NewEvent(invID)
	event.Author = author
	event.LLMResponse.Content = &genai.Content{
		Parts: []*genai.Part{{Text: text}},
	}
	return event
}

func TestProcessEventParts_FunctionCall(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	event := makeFunctionCallEvent("inv-1", "test-agent", "run_script", "call-1",
		map[string]any{"script": "ls"})

	var auditEvents []AuditEvent
	var textBuilder, resultBuilder strings.Builder
	usage := AgentUsage{}
	config := AgentConfig{}

	processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, nil)

	// FunctionCall increments ToolCallCount and records tool_call audit event.
	assert.Expect(usage.ToolCallCount).To(Equal(1))
	assert.Expect(auditEvents).To(HaveLen(1))
	assert.Expect(auditEvents[0].Type).To(Equal("tool_call"))
	assert.Expect(auditEvents[0].ToolName).To(Equal("run_script"))
	assert.Expect(auditEvents[0].ToolCallID).To(Equal("call-1"))
	assert.Expect(auditEvents[0].Author).To(Equal("test-agent"))
	assert.Expect(auditEvents[0].InvocationID).To(Equal("inv-1"))
	assert.Expect(auditEvents[0].ToolArgs).To(HaveKeyWithValue("script", "ls"))

	// No text should be written to either builder.
	assert.Expect(textBuilder.String()).To(BeEmpty())
	assert.Expect(resultBuilder.String()).To(BeEmpty())
}

func TestProcessEventParts_FunctionResponse(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	event := makeFunctionResponseEvent("inv-2", "test-agent", "run_script", "call-1",
		map[string]any{"stdout": "pr.diff", "exit_code": 0})

	var auditEvents []AuditEvent
	var textBuilder, resultBuilder strings.Builder
	usage := AgentUsage{}
	config := AgentConfig{}

	processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, nil)

	// FunctionResponse does NOT increment ToolCallCount (only FunctionCall does).
	assert.Expect(usage.ToolCallCount).To(Equal(0))
	assert.Expect(auditEvents).To(HaveLen(1))
	assert.Expect(auditEvents[0].Type).To(Equal("tool_response"))
	assert.Expect(auditEvents[0].ToolName).To(Equal("run_script"))
	assert.Expect(auditEvents[0].ToolCallID).To(Equal("call-1"))
	assert.Expect(auditEvents[0].ToolResult).To(HaveKeyWithValue("stdout", "pr.diff"))

	assert.Expect(textBuilder.String()).To(BeEmpty())
	assert.Expect(resultBuilder.String()).To(BeEmpty())
}

func TestProcessEventParts_NonFinalText(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Add a FunctionResponse so IsFinalResponse() returns false.
	event := session.NewEvent("inv-3")
	event.Author = "test-agent"
	event.LLMResponse.Content = &genai.Content{
		Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: "run_script", ID: "c1",
				Response: map[string]any{"stdout": "x"}}},
			{Text: "thinking..."},
		},
	}

	var auditEvents []AuditEvent
	var textBuilder, resultBuilder strings.Builder
	usage := AgentUsage{}
	config := AgentConfig{}

	processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, nil)

	// Text type should be "model_text" since event is not final.
	textEvents := filterAuditByType(auditEvents, "model_text")
	assert.Expect(textEvents).To(HaveLen(1))
	assert.Expect(textEvents[0].Text).To(Equal("thinking..."))

	assert.Expect(textBuilder.String()).To(Equal("thinking..."))
	// Non-final text must NOT go into resultBuilder.
	assert.Expect(resultBuilder.String()).To(BeEmpty())
}

func TestProcessEventParts_FinalText(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Text-only event with no function calls → IsFinalResponse() returns true.
	event := makeTextEvent("inv-4", "test-agent", "All done.")

	var auditEvents []AuditEvent
	var textBuilder, resultBuilder strings.Builder
	usage := AgentUsage{}
	config := AgentConfig{}

	processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, nil)

	assert.Expect(auditEvents).To(HaveLen(1))
	assert.Expect(auditEvents[0].Type).To(Equal("model_final"))
	assert.Expect(auditEvents[0].Text).To(Equal("All done."))

	// Final text goes to BOTH builders.
	assert.Expect(textBuilder.String()).To(Equal("All done."))
	assert.Expect(resultBuilder.String()).To(Equal("All done."))
}

func TestProcessEventParts_EventUsageAttachedToFirstTextOnly(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Event with two text parts; usage should be attached to the first only.
	event := session.NewEvent("inv-5")
	event.Author = "test-agent"
	event.LLMResponse.Content = &genai.Content{
		Parts: []*genai.Part{
			{Text: "first chunk"},
			{Text: "second chunk"},
		},
	}

	eventUsage := &AuditUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}

	var auditEvents []AuditEvent
	var textBuilder, resultBuilder strings.Builder
	usage := AgentUsage{}
	config := AgentConfig{}

	processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, eventUsage)

	assert.Expect(auditEvents).To(HaveLen(2))
	// Usage attached to first text event.
	assert.Expect(auditEvents[0].Usage).NotTo(BeNil())
	assert.Expect(auditEvents[0].Usage.PromptTokens).To(Equal(int32(10)))
	// Second text event has no usage.
	assert.Expect(auditEvents[1].Usage).To(BeNil())
}

func TestProcessEventParts_MultipleParts(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// One event with FunctionCall + FunctionResponse + Text parts.
	// FunctionResponse makes isFinal=false so text is "model_text".
	event := session.NewEvent("inv-6")
	event.Author = "agent"
	event.LLMResponse.Content = &genai.Content{
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "tool-a", ID: "c-a"}},
			{FunctionResponse: &genai.FunctionResponse{Name: "tool-a", ID: "c-a",
				Response: map[string]any{"result": "ok"}}},
			{Text: "intermediate"},
		},
	}

	var auditEvents []AuditEvent
	var textBuilder, resultBuilder strings.Builder
	usage := AgentUsage{}
	config := AgentConfig{}

	processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, nil)

	assert.Expect(usage.ToolCallCount).To(Equal(1))

	types := make([]string, 0, len(auditEvents))
	for _, e := range auditEvents {
		types = append(types, e.Type)
	}
	assert.Expect(types).To(ConsistOf("tool_call", "tool_response", "model_text"))

	assert.Expect(textBuilder.String()).To(Equal("intermediate"))
	assert.Expect(resultBuilder.String()).To(BeEmpty())
}

func TestProcessEventParts_OnAuditEventCallback(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	event := makeTextEvent("inv-7", "agent", "result text")

	var auditEvents []AuditEvent
	var textBuilder, resultBuilder strings.Builder
	usage := AgentUsage{}

	var streamed []AuditEvent
	config := AgentConfig{
		OnAuditEvent: func(e AuditEvent) { streamed = append(streamed, e) },
	}

	processEventParts(event, &usage, &auditEvents, &textBuilder, &resultBuilder, config, nil)

	// The callback must fire for each appended event.
	assert.Expect(streamed).To(HaveLen(1))
	assert.Expect(streamed[0].Type).To(Equal("model_final"))
	assert.Expect(streamed[0].Text).To(Equal("result text"))
}

func TestProcessEventParts_ToolCallIncrementsOnlyForFunctionCall(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Three events: FunctionCall, FunctionResponse, text. Only FunctionCall
	// should increment ToolCallCount.
	usage := AgentUsage{}
	config := AgentConfig{}

	events := []*session.Event{
		makeFunctionCallEvent("inv-a", "agent", "tool-x", "cx", nil),
		makeFunctionResponseEvent("inv-a", "agent", "tool-x", "cx", nil),
		makeTextEvent("inv-a", "agent", "done"),
	}

	for _, ev := range events {
		var auditEvents []AuditEvent
		var tb, rb strings.Builder
		processEventParts(ev, &usage, &auditEvents, &tb, &rb, config, nil)
	}

	assert.Expect(usage.ToolCallCount).To(Equal(1))
}

// filterAuditByType is a test helper that returns all events with the given type.
func filterAuditByType(events []AuditEvent, typ string) []AuditEvent {
	var out []AuditEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}
