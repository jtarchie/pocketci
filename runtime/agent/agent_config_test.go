package agent_test

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/runtime/agent"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
)

func TestToolDefJSON(t *testing.T) {
	t.Parallel()

	t.Run("tools field roundtrips through JSON", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:   "orchestrator",
			Prompt: "call your tools",
			Model:  "anthropic/claude-sonnet-4-20250514",
			Image:  "alpine/git",
			Tools: []agent.ToolDef{
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

		assert.Expect(decoded.Tools).To(HaveLen(2))
		assert.Expect(decoded.Tools[0].Name).To(Equal("code-quality-reviewer"))
		assert.Expect(decoded.Tools[0].Prompt).To(Equal("Review for code quality"))
		assert.Expect(decoded.Tools[0].StorageKeyPrefix).To(Equal("/pipeline/run-1/jobs/review-pr/2/agent/orchestrator"))
		assert.Expect(decoded.Tools[1].Name).To(Equal("security-reviewer"))
	})

	t.Run("tools uses snake_case JSON key", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:  "parent",
			Image: "alpine",
			Tools: []agent.ToolDef{
				{Name: "child", Image: "alpine"},
			},
		}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		raw := make(map[string]json.RawMessage)
		assert.Expect(json.Unmarshal(data, &raw)).To(Succeed())
		_, found := raw["tools"]
		assert.Expect(found).To(BeTrue(), "tools key must appear in JSON")
		_, wrongKey := raw["Tools"]
		assert.Expect(wrongKey).To(BeFalse(), "Tools (Go name) must not appear in JSON")
	})

	t.Run("tools omitted from JSON when empty", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{Name: "agent", Image: "alpine"}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		raw := make(map[string]json.RawMessage)
		assert.Expect(json.Unmarshal(data, &raw)).To(Succeed())
		_, found := raw["tools"]
		assert.Expect(found).To(BeFalse(), "tools must be omitted when empty")
	})

	t.Run("ToolDef image field roundtrips for own-container mode", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:  "orchestrator",
			Image: "alpine/git",
			Tools: []agent.ToolDef{
				{Name: "shared-reviewer", Prompt: "Uses parent container"},
				{Name: "custom-reviewer", Prompt: "Uses own container", Image: "python:3.12"},
			},
		}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		var decoded agent.AgentConfig
		assert.Expect(json.Unmarshal(data, &decoded)).To(Succeed())

		assert.Expect(decoded.Tools).To(HaveLen(2))

		// Shared: image empty, will default to parent at runtime.
		assert.Expect(decoded.Tools[0].Name).To(Equal("shared-reviewer"))
		assert.Expect(decoded.Tools[0].Image).To(BeEmpty())

		// Own-container: image preserved through roundtrip.
		assert.Expect(decoded.Tools[1].Name).To(Equal("custom-reviewer"))
		assert.Expect(decoded.Tools[1].Image).To(Equal("python:3.12"))
	})

	t.Run("ToolDef storageKeyPrefix roundtrips", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		tool := agent.ToolDef{
			Name:             "my-reviewer",
			StorageKeyPrefix: "/pipeline/run-42/jobs/review-pr/3/agent/orchestrator",
		}

		data, err := json.Marshal(tool)
		assert.Expect(err).NotTo(HaveOccurred())

		var decoded agent.ToolDef
		assert.Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		assert.Expect(decoded.StorageKeyPrefix).To(Equal(tool.StorageKeyPrefix))
	})

	t.Run("task tool roundtrips through JSON", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:  "orchestrator",
			Image: "alpine/git",
			Tools: []agent.ToolDef{
				{
					Name:        "run-linter",
					IsTask:      true,
					Description: "Run the project linter",
					Image:       "golangci/golangci-lint",
					CommandPath: "golangci-lint",
					CommandArgs: []string{"run", "./..."},
					Env:         map[string]string{"GOPROXY": "off"},
				},
			},
		}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		var decoded agent.AgentConfig
		assert.Expect(json.Unmarshal(data, &decoded)).To(Succeed())

		assert.Expect(decoded.Tools).To(HaveLen(1))
		tool := decoded.Tools[0]
		assert.Expect(tool.Name).To(Equal("run-linter"))
		assert.Expect(tool.IsTask).To(BeTrue())
		assert.Expect(tool.Description).To(Equal("Run the project linter"))
		assert.Expect(tool.CommandPath).To(Equal("golangci-lint"))
		assert.Expect(tool.CommandArgs).To(Equal([]string{"run", "./..."}))
		assert.Expect(tool.Env).To(HaveKeyWithValue("GOPROXY", "off"))
	})
}

func TestOutputSchemaRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("output_schema roundtrips through JSON", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{
			Name:  "review-agent",
			Model: "openrouter/google/gemini-3",
			Image: "alpine/git",
			OutputSchema: map[string]interface{}{
				"summary": "string",
				"issues[]": map[string]interface{}{
					"severity":    "critical|high|medium|low",
					"description": "string",
				},
			},
		}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		var decoded agent.AgentConfig
		assert.Expect(json.Unmarshal(data, &decoded)).To(Succeed())

		assert.Expect(decoded.OutputSchema).NotTo(BeNil())
		assert.Expect(decoded.OutputSchema).To(HaveKey("summary"))
		assert.Expect(decoded.OutputSchema).To(HaveKey("issues[]"))
	})

	t.Run("output_schema omitted from JSON when nil", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		config := agent.AgentConfig{Name: "agent", Image: "alpine"}

		data, err := json.Marshal(config)
		assert.Expect(err).NotTo(HaveOccurred())

		raw := make(map[string]json.RawMessage)
		assert.Expect(json.Unmarshal(data, &raw)).To(Succeed())
		_, found := raw["outputSchema"]
		assert.Expect(found).To(BeFalse(), "outputSchema must be omitted when nil")
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
