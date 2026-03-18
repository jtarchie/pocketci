package agent

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
)

func TestAuditEventJSONTags(t *testing.T) {
	t.Parallel()

	t.Run("tool_call event serialises all fields", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		ae := AuditEvent{
			Timestamp:    "2026-03-07T12:00:00Z",
			InvocationID: "inv-1",
			Author:       "my-agent",
			Type:         "tool_call",
			ToolName:     "run_script",
			ToolCallID:   "call-abc",
			ToolArgs:     map[string]any{"command": "ls"},
		}

		b, err := json.Marshal(ae)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(m["timestamp"]).To(Equal("2026-03-07T12:00:00Z"))
		assert.Expect(m["invocationId"]).To(Equal("inv-1"))
		assert.Expect(m["author"]).To(Equal("my-agent"))
		assert.Expect(m["type"]).To(Equal("tool_call"))
		assert.Expect(m["toolName"]).To(Equal("run_script"))
		assert.Expect(m["toolCallId"]).To(Equal("call-abc"))
		assert.Expect(m["toolArgs"]).NotTo(BeNil())

		// Fields not set must be omitted.
		_, hasText := m["text"]
		assert.Expect(hasText).To(BeFalse())
		_, hasResult := m["toolResult"]
		assert.Expect(hasResult).To(BeFalse())
		_, hasUsage := m["usage"]
		assert.Expect(hasUsage).To(BeFalse())
	})

	t.Run("tool_response event serialises result and omits args", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		ae := AuditEvent{
			Type:       "tool_response",
			ToolName:   "run_script",
			ToolCallID: "call-abc",
			ToolResult: map[string]any{"stdout": "hello", "exit_code": float64(0)},
		}

		b, err := json.Marshal(ae)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(m["type"]).To(Equal("tool_response"))
		assert.Expect(m["toolResult"]).NotTo(BeNil())

		_, hasArgs := m["toolArgs"]
		assert.Expect(hasArgs).To(BeFalse())
	})

	t.Run("model_final event with text and usage", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		ae := AuditEvent{
			Type:   "model_final",
			Author: "my-agent",
			Text:   "All done.",
			Usage: &AuditUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}

		b, err := json.Marshal(ae)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(m["type"]).To(Equal("model_final"))
		assert.Expect(m["text"]).To(Equal("All done."))

		usage, ok := m["usage"].(map[string]any)
		assert.Expect(ok).To(BeTrue())
		assert.Expect(usage["promptTokens"]).To(BeNumerically("==", 100))
		assert.Expect(usage["completionTokens"]).To(BeNumerically("==", 50))
		assert.Expect(usage["totalTokens"]).To(BeNumerically("==", 150))
	})

	t.Run("nil usage is omitted", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		ae := AuditEvent{
			Type: "model_text",
			Text: "thinking...",
		}

		b, err := json.Marshal(ae)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		_, hasUsage := m["usage"]
		assert.Expect(hasUsage).To(BeFalse())
	})

	t.Run("user_message event omits tool fields", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		ae := AuditEvent{
			Type:   "user_message",
			Author: "user",
			Text:   "Please run the tests.",
		}

		b, err := json.Marshal(ae)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(m["type"]).To(Equal("user_message"))
		assert.Expect(m["text"]).To(Equal("Please run the tests."))

		for _, absent := range []string{"toolName", "toolCallId", "toolArgs", "toolResult", "invocationId", "usage"} {
			_, found := m[absent]
			assert.Expect(found).To(BeFalse(), "expected %q to be absent", absent)
		}
	})

	t.Run("pre_context list_tasks omits empty args", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		// list_tasks has no input args; the empty map is omitted by omitempty.
		ae := AuditEvent{
			Timestamp:  "2026-03-07T12:00:00Z",
			Author:     "my-agent",
			Type:       "pre_context",
			ToolName:   "list_tasks",
			ToolArgs:   map[string]any{},
			ToolResult: map[string]any{"tasks": []any{}},
		}

		b, err := json.Marshal(ae)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(m["type"]).To(Equal("pre_context"))
		assert.Expect(m["toolName"]).To(Equal("list_tasks"))
		assert.Expect(m["toolResult"]).NotTo(BeNil())

		// Empty toolArgs is omitted via omitempty.
		_, hasArgs := m["toolArgs"]
		assert.Expect(hasArgs).To(BeFalse())

		// invocationId must be absent for pre-context entries.
		_, hasInv := m["invocationId"]
		assert.Expect(hasInv).To(BeFalse())
	})

	t.Run("pre_context get_task_result includes non-empty args", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		ae := AuditEvent{
			Timestamp:  "2026-03-07T12:00:00Z",
			Author:     "my-agent",
			Type:       "pre_context",
			ToolName:   "get_task_result",
			ToolArgs:   map[string]any{"name": "build"},
			ToolResult: map[string]any{"name": "build", "status": "success", "stdout": "ok"},
		}

		b, err := json.Marshal(ae)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(m["toolName"]).To(Equal("get_task_result"))
		assert.Expect(m["toolArgs"]).NotTo(BeNil())
		assert.Expect(m["toolResult"]).NotTo(BeNil())
	})
}

func TestAuditUsageJSONTags(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	au := AuditUsage{
		PromptTokens:     200,
		CompletionTokens: 80,
		TotalTokens:      280,
	}

	b, err := json.Marshal(au)
	assert.Expect(err).NotTo(HaveOccurred())

	var m map[string]any
	err = json.Unmarshal(b, &m)
	assert.Expect(err).NotTo(HaveOccurred())

	assert.Expect(m).To(HaveKey("promptTokens"))
	assert.Expect(m).To(HaveKey("completionTokens"))
	assert.Expect(m).To(HaveKey("totalTokens"))
	assert.Expect(m).NotTo(HaveKey("prompt_tokens"))
	assert.Expect(m).NotTo(HaveKey("completion_tokens"))
}

func TestAgentResultAuditLog(t *testing.T) {
	t.Parallel()

	t.Run("populated", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		result := AgentResult{
			Text:   "done",
			Status: "success",
			AuditLog: []AuditEvent{
				{Type: "user_message", Author: "user", Text: "go"},
				{Type: "model_final", Author: "agent", Text: "done"},
			},
		}

		b, err := json.Marshal(result)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		log, ok := m["auditLog"].([]any)
		assert.Expect(ok).To(BeTrue())
		assert.Expect(log).To(HaveLen(2))

		first := log[0].(map[string]any)
		assert.Expect(first["type"]).To(Equal("user_message"))

		second := log[1].(map[string]any)
		assert.Expect(second["type"]).To(Equal("model_final"))
	})

	t.Run("nil_is_present", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		result := AgentResult{
			Text:   "done",
			Status: "success",
		}

		b, err := json.Marshal(result)
		assert.Expect(err).NotTo(HaveOccurred())

		var m map[string]any
		err = json.Unmarshal(b, &m)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(m).To(HaveKey("auditLog"))
	})
}

func TestAuditEventTypes(t *testing.T) {
	t.Parallel()

	// Enumerate known type strings and verify they round-trip through JSON.
	knownTypes := []string{
		"pre_context",
		"user_message",
		"tool_call",
		"tool_response",
		"model_text",
		"model_final",
	}

	for _, typ := range knownTypes {
		typ := typ

		t.Run(typ, func(t *testing.T) {
			t.Parallel()

			assert := NewGomegaWithT(t)

			ae := AuditEvent{Type: typ, Text: "x"}
			b, err := json.Marshal(ae)
			assert.Expect(err).NotTo(HaveOccurred())

			var got AuditEvent
			err = json.Unmarshal(b, &got)
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(got.Type).To(Equal(typ))
		})
	}
}
