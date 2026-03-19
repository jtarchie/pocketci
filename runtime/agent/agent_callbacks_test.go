package agent_test

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/runtime/agent"
)

func TestProgressiveCallbackEmission(t *testing.T) {
	t.Parallel()

	t.Run("onAuditEvent invoked for each event append", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		var capturedEvents []agent.AuditEvent
		onAudit := func(event agent.AuditEvent) {
			capturedEvents = append(capturedEvents, event)
		}

		var events []agent.AuditEvent

		agent.AppendAuditEvent(&events, agent.AuditEvent{Type: "user_message", Text: "start"}, onAudit)
		agent.AppendAuditEvent(&events, agent.AuditEvent{Type: "tool_call", ToolName: "run_script"}, onAudit)
		agent.AppendAuditEvent(&events, agent.AuditEvent{Type: "model_final", Text: "done"}, onAudit)

		assert.Expect(capturedEvents).To(HaveLen(3))
		assert.Expect(capturedEvents[0].Type).To(Equal("user_message"))
		assert.Expect(capturedEvents[1].Type).To(Equal("tool_call"))
		assert.Expect(capturedEvents[2].Type).To(Equal("model_final"))
	})

	t.Run("onAuditEvent tolerates nil callback", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		var events []agent.AuditEvent

		agent.AppendAuditEvent(&events, agent.AuditEvent{Type: "user_message"}, nil)

		assert.Expect(events).To(HaveLen(1))
	})

	t.Run("onUsage invoked on usage snapshot", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		var capturedUsage []agent.AgentUsage
		onUsage := func(usage agent.AgentUsage) {
			capturedUsage = append(capturedUsage, usage)
		}

		agent.EmitUsageSnapshot(onUsage, agent.AgentUsage{TotalTokens: 100, LLMRequests: 1})
		agent.EmitUsageSnapshot(onUsage, agent.AgentUsage{TotalTokens: 250, LLMRequests: 2})

		assert.Expect(capturedUsage).To(HaveLen(2))
		assert.Expect(capturedUsage[1].TotalTokens).To(Equal(int32(250)))
		assert.Expect(capturedUsage[1].LLMRequests).To(Equal(2))
	})

	t.Run("onUsage tolerates nil callback", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		assert.Expect(func() {
			agent.EmitUsageSnapshot(nil, agent.AgentUsage{TotalTokens: 50})
		}).NotTo(Panic())
	})
}
