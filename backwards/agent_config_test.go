package backwards_test

import (
	"os"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/jtarchie/pocketci/backwards"
	. "github.com/onsi/gomega"
)

func TestAgentStepConfig(t *testing.T) {
	t.Parallel()

	t.Run("parses agent step YAML fields correctly", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		contents, err := os.ReadFile("testdata/agent-with-llm.yml")
		assert.Expect(err).NotTo(HaveOccurred())

		var config backwards.Config

		err = yaml.Unmarshal(contents, &config)
		assert.Expect(err).NotTo(HaveOccurred())

		assert.Expect(config.Jobs).To(HaveLen(1))

		step := config.Jobs[0].Plan[1]

		assert.Expect(step.Agent).To(Equal("review-code-agent"))
		assert.Expect(step.Model).To(ContainSubstring("gemini"))

		// LLM config
		assert.Expect(step.AgentLLM).NotTo(BeNil())
		assert.Expect(*step.AgentLLM.Temperature).To(BeNumerically("~", 0.2, 0.001))
		assert.Expect(step.AgentLLM.MaxTokens).To(Equal(int32(8192)))

		// Thinking config
		assert.Expect(step.AgentThinking).NotTo(BeNil())
		assert.Expect(step.AgentThinking.Budget).To(Equal(int32(10000)))
		assert.Expect(step.AgentThinking.Level).To(Equal("medium"))

		// Safety config
		assert.Expect(step.AgentSafety).NotTo(BeEmpty())
		assert.Expect(step.AgentSafety["harassment"]).To(Equal("block_none"))
		assert.Expect(step.AgentSafety["dangerous_content"]).To(Equal("block_none"))

		// Context guard config
		assert.Expect(step.AgentContextGuard).NotTo(BeNil())
		assert.Expect(step.AgentContextGuard.Strategy).To(Equal("threshold"))
		assert.Expect(step.AgentContextGuard.MaxTokens).To(Equal(100000))

		// Limits config
		assert.Expect(step.AgentLimits).NotTo(BeNil())
		assert.Expect(step.AgentLimits.MaxTurns).To(Equal(20))
		assert.Expect(step.AgentLimits.MaxTotalTokens).To(Equal(int32(500000)))
	})

	t.Run("NewPipeline transpiles agent step with LLM config to JS", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		js, err := backwards.NewPipeline("testdata/agent-with-llm.yml")
		assert.Expect(err).NotTo(HaveOccurred())

		// The generated JS should include config fields so job_runner.ts can read them.
		assert.Expect(js).To(ContainSubstring("review-code-agent"))
		assert.Expect(js).To(ContainSubstring(`"temperature"`))
		assert.Expect(js).To(ContainSubstring(`"max_tokens"`))
		assert.Expect(js).To(ContainSubstring(`"thinking"`))
		assert.Expect(js).To(ContainSubstring(`"context_guard"`))
		assert.Expect(js).To(ContainSubstring(`"safety"`))
		assert.Expect(js).To(ContainSubstring(`"limits"`))
	})

	t.Run("agent step without LLM config is still valid", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		minimalYAML := `
jobs:
  - name: review
    plan:
      - agent: my-agent
        prompt: Do something
        model: openrouter/google/gemini-3.1-flash-lite-preview
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          run:
            path: echo
`
		var config backwards.Config

		err := yaml.Unmarshal([]byte(minimalYAML), &config)
		assert.Expect(err).NotTo(HaveOccurred())

		step := config.Jobs[0].Plan[0]
		assert.Expect(step.Agent).To(Equal("my-agent"))
		assert.Expect(step.AgentLLM).To(BeNil())
		assert.Expect(step.AgentThinking).To(BeNil())
		assert.Expect(step.AgentSafety).To(BeEmpty())
		assert.Expect(step.AgentContextGuard).To(BeNil())
	})

	t.Run("parses agent step with file field", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		fileYAML := `
jobs:
  - name: review
    plan:
      - agent: my-agent
        file: repo/agents/review.yml
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: repo
`
		var config backwards.Config

		err := yaml.Unmarshal([]byte(fileYAML), &config)
		assert.Expect(err).NotTo(HaveOccurred())

		step := config.Jobs[0].Plan[0]
		assert.Expect(step.Agent).To(Equal("my-agent"))
		assert.Expect(step.File).To(Equal("repo/agents/review.yml"))
	})

	t.Run("parses agent step with prompt_file field", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		promptFileYAML := `
jobs:
  - name: review
    plan:
      - agent: my-agent
        prompt_file: repo/prompts/review.md
        model: openrouter/google/gemini-3.1-flash-lite-preview
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: repo
`
		var config backwards.Config

		err := yaml.Unmarshal([]byte(promptFileYAML), &config)
		assert.Expect(err).NotTo(HaveOccurred())

		step := config.Jobs[0].Plan[0]
		assert.Expect(step.Agent).To(Equal("my-agent"))
		assert.Expect(step.PromptFile).To(Equal("repo/prompts/review.md"))
		assert.Expect(step.Prompt).To(BeEmpty())
	})

	t.Run("transpiles agent step with file field to JS", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		js, err := backwards.NewPipelineFromContent(`
jobs:
  - name: review
    plan:
      - agent: my-agent
        file: repo/agents/review.yml
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: repo
`)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(js).To(ContainSubstring("my-agent"))
		assert.Expect(js).To(ContainSubstring("repo/agents/review.yml"))
	})

	t.Run("transpiles agent step with prompt_file field to JS", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		js, err := backwards.NewPipelineFromContent(`
jobs:
  - name: review
    plan:
      - agent: my-agent
        prompt_file: repo/prompts/review.md
        model: openrouter/google/gemini-3.1-flash-lite-preview
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: repo
`)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(js).To(ContainSubstring("my-agent"))
		assert.Expect(js).To(ContainSubstring("repo/prompts/review.md"))
	})

	t.Run("validation rejects agent step without prompt, file, or prompt_file", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		_, err := backwards.NewPipelineFromContent(`
jobs:
  - name: review
    plan:
      - agent: my-agent
        model: openrouter/google/gemini-3.1-flash-lite-preview
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
`)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("agent step"))
		assert.Expect(err.Error()).To(ContainSubstring("requires prompt, prompt_file, file, or uri"))
	})

	t.Run("validation accepts agent step with only file", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		_, err := backwards.NewPipelineFromContent(`
jobs:
  - name: review
    plan:
      - agent: my-agent
        file: repo/agents/review.yml
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: repo
`)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("validation accepts agent step with only prompt_file", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		_, err := backwards.NewPipelineFromContent(`
jobs:
  - name: review
    plan:
      - agent: my-agent
        prompt_file: repo/prompts/review.md
        model: openrouter/google/gemini-3.1-flash-lite-preview
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine }
          inputs:
            - name: repo
`)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("pr-review.yml example validates successfully", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		contents, err := os.ReadFile("../examples/agent/pr-review.yml")
		assert.Expect(err).NotTo(HaveOccurred())

		err = backwards.ValidatePipeline(contents)
		assert.Expect(err).NotTo(HaveOccurred())
	})

	t.Run("extracted agent config files are valid YAML", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		agentFiles := []string{
			"../examples/agent/agents/specialist-reviewer.yml",
			"../examples/agent/agents/final-reviewer.yml",
		}

		for _, f := range agentFiles {
			contents, err := os.ReadFile(f)
			assert.Expect(err).NotTo(HaveOccurred())

			var config map[string]any
			err = yaml.Unmarshal(contents, &config)
			assert.Expect(err).NotTo(HaveOccurred())

			// Each agent config must have a prompt and model
			assert.Expect(config).To(HaveKey("prompt"), "file %s missing prompt", f)
			assert.Expect(config).To(HaveKey("model"), "file %s missing model", f)
		}
	})

	t.Run("extracted task config file is valid YAML", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		taskFiles := []string{
			"../examples/agent/tasks/post-comment.yml",
			"../examples/agent/tasks/generate-diff.yml",
		}

		for _, f := range taskFiles {
			contents, err := os.ReadFile(f)
			assert.Expect(err).NotTo(HaveOccurred())

			var config backwards.TaskConfig
			err = yaml.Unmarshal(contents, &config)
			assert.Expect(err).NotTo(HaveOccurred())

			assert.Expect(config.Run).NotTo(BeNil(), "file %s missing run", f)
			assert.Expect(config.Run.Path).NotTo(BeEmpty(), "file %s missing run.path", f)
		}
	})

	t.Run("parses agent step with tools field", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		var config backwards.Config
		err := yaml.Unmarshal([]byte(`
jobs:
  - name: review
    plan:
      - agent: orchestrator
        file: repo/agents/orchestrator.yml
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine/git }
          inputs:
            - name: repo
        tools:
          - agent: code-quality-reviewer
            file: repo/agents/code-quality.yml
          - agent: security-reviewer
            prompt: Check for security issues
            model: openrouter/google/gemini-3.1-flash-lite-preview
`), &config)
		assert.Expect(err).NotTo(HaveOccurred())

		step := config.Jobs[0].Plan[0]
		assert.Expect(step.Agent).To(Equal("orchestrator"))
		assert.Expect(step.Tools).To(HaveLen(2))
		assert.Expect(step.Tools[0].Agent).To(Equal("code-quality-reviewer"))
		assert.Expect(step.Tools[0].File).To(Equal("repo/agents/code-quality.yml"))
		assert.Expect(step.Tools[1].Agent).To(Equal("security-reviewer"))
		assert.Expect(step.Tools[1].Prompt).To(Equal("Check for security issues"))
	})

	t.Run("parses tool with own container image", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		var config backwards.Config
		err := yaml.Unmarshal([]byte(`
jobs:
  - name: review
    plan:
      - agent: orchestrator
        prompt: Call your sub-agents
        model: anthropic/claude-sonnet-4-20250514
        config:
          platform: linux
          image: alpine/git
        tools:
          - agent: shared-reviewer
            prompt: Uses parent container
          - agent: custom-reviewer
            prompt: Uses own container
            config:
              platform: linux
              image: python:3.12
`), &config)
		assert.Expect(err).NotTo(HaveOccurred())

		step := config.Jobs[0].Plan[0]
		assert.Expect(step.Tools).To(HaveLen(2))

		// Shared: no config block, will default to parent image at runtime.
		assert.Expect(step.Tools[0].Agent).To(Equal("shared-reviewer"))
		assert.Expect(step.Tools[0].TaskConfig).To(BeNil())

		// Own container: has config.image set.
		assert.Expect(step.Tools[1].Agent).To(Equal("custom-reviewer"))
		assert.Expect(step.Tools[1].TaskConfig).NotTo(BeNil())
		assert.Expect(step.Tools[1].TaskConfig.Image).To(Equal("python:3.12"))
	})

	t.Run("transpiles tool with own container image to JS", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		js, err := backwards.NewPipelineFromContent(`
jobs:
  - name: review
    plan:
      - agent: orchestrator
        prompt: Call your sub-agents
        model: anthropic/claude-sonnet-4-20250514
        config:
          platform: linux
          image: alpine/git
        tools:
          - agent: shared-reviewer
            prompt: Uses parent container
          - agent: custom-reviewer
            prompt: Uses own container
            config:
              platform: linux
              image: python:3.12
`)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(js).To(ContainSubstring("shared-reviewer"))
		assert.Expect(js).To(ContainSubstring("custom-reviewer"))
		assert.Expect(js).To(ContainSubstring("python:3.12"))
	})

	t.Run("transpiles agent step with tools to JS", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		js, err := backwards.NewPipelineFromContent(`
jobs:
  - name: review
    plan:
      - agent: orchestrator
        file: repo/agents/orchestrator.yml
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: alpine/git }
          inputs:
            - name: repo
        tools:
          - agent: code-quality-reviewer
            file: repo/agents/code-quality.yml
          - agent: security-reviewer
            prompt: Check for security issues
            model: openrouter/google/gemini-3.1-flash-lite-preview
`)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(js).To(ContainSubstring("orchestrator"))
		assert.Expect(js).To(ContainSubstring("tools"))
		assert.Expect(js).To(ContainSubstring("code-quality-reviewer"))
		assert.Expect(js).To(ContainSubstring("security-reviewer"))
	})
}
