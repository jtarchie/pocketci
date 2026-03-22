package agent

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
