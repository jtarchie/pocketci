package agent_test

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/runtime/agent"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
)

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
