package agent_test

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/runtime/agent"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
)

func TestSubAgentConfigJSON(t *testing.T) {
	t.Parallel()

	t.Run("sub_agents field roundtrips through JSON", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:   "orchestrator",
			Prompt: "call your sub-agents",
			Model:  "anthropic/claude-sonnet-4-20250514",
			Image:  "alpine/git",
			SubAgents: []agent.SubAgentConfig{
				{
					Name:             "code-quality-reviewer",
					Prompt:           "Review for code quality",
					Model:            "anthropic/claude-sonnet-4-20250514",
					Image:            "alpine/git",
					StorageKeyPrefix: "/pipeline/run-1/jobs/review-pr/2/agent/orchestrator",
				},
				{
					Name:   "security-reviewer",
					Prompt: "Audit for security issues",
					Image:  "alpine/git",
				},
			},
		}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		var decoded agent.AgentConfig
		assert.Expect(json.Unmarshal(data, &decoded)).To(Succeed())

		assert.Expect(decoded.SubAgents).To(HaveLen(2))
		assert.Expect(decoded.SubAgents[0].Name).To(Equal("code-quality-reviewer"))
		assert.Expect(decoded.SubAgents[0].Prompt).To(Equal("Review for code quality"))
		assert.Expect(decoded.SubAgents[0].StorageKeyPrefix).To(Equal("/pipeline/run-1/jobs/review-pr/2/agent/orchestrator"))
		assert.Expect(decoded.SubAgents[1].Name).To(Equal("security-reviewer"))
	})

	t.Run("sub_agents uses snake_case JSON key", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:  "parent",
			Image: "alpine",
			SubAgents: []agent.SubAgentConfig{
				{Name: "child", Image: "alpine"},
			},
		}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		raw := make(map[string]json.RawMessage)
		assert.Expect(json.Unmarshal(data, &raw)).To(Succeed())
		_, found := raw["sub_agents"]
		assert.Expect(found).To(BeTrue(), "sub_agents key must appear in JSON")
		_, wrongKey := raw["SubAgents"]
		assert.Expect(wrongKey).To(BeFalse(), "SubAgents (Go name) must not appear in JSON")
	})

	t.Run("sub_agents omitted from JSON when empty", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{Name: "agent", Image: "alpine"}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		raw := make(map[string]json.RawMessage)
		assert.Expect(json.Unmarshal(data, &raw)).To(Succeed())
		_, found := raw["sub_agents"]
		assert.Expect(found).To(BeFalse(), "sub_agents must be omitted when empty")
	})

	t.Run("SubAgentConfig storageKeyPrefix roundtrips", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		sub := agent.SubAgentConfig{
			Name:             "my-reviewer",
			StorageKeyPrefix: "/pipeline/run-42/jobs/review-pr/3/agent/orchestrator",
		}

		data, err := json.Marshal(sub)
		assert.Expect(err).NotTo(HaveOccurred())

		var decoded agent.SubAgentConfig
		assert.Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		assert.Expect(decoded.StorageKeyPrefix).To(Equal(sub.StorageKeyPrefix))
	})
}

func TestAgentConfigJSONRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("internal fields are excluded from JSON", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:   "test-agent",
			Prompt: "do something",
			Model:  "anthropic/claude-sonnet-4-20250514",
			Image:  "alpine:latest",
			Mounts: map[string]pipelinerunner.VolumeResult{
				"repo": {Name: "abc123", Path: "repo"},
			},
			OutputVolumePath: "output",
			// Internal fields that must not appear in JSON.
			Namespace:   "ci-test",
			RunID:       "run-123",
			PipelineID:  "pipe-456",
			TriggeredBy: "webhook",
		}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		raw := make(map[string]json.RawMessage)
		assert.Expect(json.Unmarshal(data, &raw)).To(Succeed())

		// Internal fields must be absent from the serialized JSON.
		for _, field := range []string{"Storage", "Namespace", "RunID", "PipelineID", "TriggeredBy"} {
			_, found := raw[field]
			assert.Expect(found).To(BeFalse(), "internal field %q must not appear in JSON", field)
		}

		// Public fields must be present.
		for _, field := range []string{"name", "prompt", "model", "image", "mounts", "outputVolumePath"} {
			_, found := raw[field]
			assert.Expect(found).To(BeTrue(), "public field %q must appear in JSON", field)
		}
	})

	t.Run("unmarshal ignores unknown internal fields", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		// Simulate JSON that might include Storage as an object (the bug scenario).
		input := `{
			"name": "test-agent",
			"prompt": "review code",
			"model": "anthropic/claude-sonnet-4-20250514",
			"image": "alpine:latest",
			"Storage": {"type": "sqlite"},
			"Namespace": "ci-ns",
			"RunID": "run-x",
			"PipelineID": "pipe-y",
			"TriggeredBy": "manual"
		}`

		var config agent.AgentConfig
		err := json.Unmarshal([]byte(input), &config)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(config.Name).To(Equal("test-agent"))
		assert.Expect(config.Prompt).To(Equal("review code"))
		assert.Expect(config.Model).To(Equal("anthropic/claude-sonnet-4-20250514"))

		// Internal fields must remain zero-valued after unmarshal.
		assert.Expect(config.Storage).To(BeNil())
		assert.Expect(config.Namespace).To(BeEmpty())
		assert.Expect(config.RunID).To(BeEmpty())
		assert.Expect(config.PipelineID).To(BeEmpty())
		assert.Expect(config.TriggeredBy).To(BeEmpty())
	})
}
