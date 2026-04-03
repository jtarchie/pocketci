package backwards_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jtarchie/pocketci/runtime/agent"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

// mockAgentRunner returns an agentRunFunc that captures the config and returns
// a canned result. It fires OnUsage and OnOutput callbacks if provided.
func mockAgentRunner(
	captured *agent.AgentConfig,
	result *agent.AgentResult,
) backwards.AgentRunFunc {
	return func(
		ctx context.Context,
		runner pipelinerunner.Runner,
		sm secrets.Manager,
		pipelineID string,
		cfg agent.AgentConfig,
	) (*agent.AgentResult, error) {
		*captured = cfg

		if cfg.OnUsage != nil {
			cfg.OnUsage(result.Usage)
		}

		if cfg.OnOutput != nil {
			cfg.OnOutput("stdout", result.Text)
		}

		return result, nil
	}
}

func mockAgentRunnerWithError(errMsg string) backwards.AgentRunFunc {
	return func(
		ctx context.Context,
		runner pipelinerunner.Runner,
		sm secrets.Manager,
		pipelineID string,
		cfg agent.AgentConfig,
	) (*agent.AgentResult, error) {
		if cfg.OnOutput != nil {
			cfg.OnOutput("stdout", "partial output")
		}

		return nil, fmt.Errorf("%s", errMsg)
	}
}

func TestAgentBasic(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/agent_basic.yml")

			logger := discardLogger()

			driver, err := df.new("test-agent-basic-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-basic", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			var captured agent.AgentConfig

			result := &agent.AgentResult{
				Text:   "Hello, I am an agent!",
				Status: "success",
				Usage: agent.AgentUsage{
					PromptTokens:     100,
					CompletionTokens: 50,
					TotalTokens:      150,
					LLMRequests:      1,
				},
			}

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{
				AgentRunFunc: mockAgentRunner(&captured, result),
			})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify agent config was correctly assembled.
			assert.Expect(captured.Name).To(Equal("test-agent"))
			assert.Expect(captured.Prompt).To(Equal("Say hello"))
			assert.Expect(captured.Model).To(Equal("test/mock-model"))
			assert.Expect(captured.Image).To(Equal("busybox"))

			// Verify storage has success state.
			val, err := store.Get(context.Background(), "/pipeline/test-run/jobs/agent-test/1/run")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(val).To(HaveKeyWithValue("status", "success"))
			assert.Expect(val).To(HaveKey("elapsed"))
			assert.Expect(val).To(HaveKeyWithValue("stdout", "Hello, I am an agent!"))

			// Verify job-level success.
			jobVal, err := store.Get(context.Background(), "/pipeline/test-run/jobs/agent-test")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(jobVal).To(HaveKeyWithValue("status", "success"))
		})
	}
}

func TestAgentWithVolumes(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/agent_with_volumes.yml")

			logger := discardLogger()

			driver, err := df.new("test-agent-vols-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-vols", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			var captured agent.AgentConfig

			result := &agent.AgentResult{
				Text:   "Read the data successfully",
				Status: "success",
			}

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{
				AgentRunFunc: mockAgentRunner(&captured, result),
			})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify input mount was passed.
			assert.Expect(captured.Mounts).To(HaveKey("my-data"))

			// Verify auto-created output volume.
			assert.Expect(captured.Mounts).To(HaveKey("vol-agent"))
			assert.Expect(captured.OutputVolumePath).To(Equal("vol-agent"))
		})
	}
}

func TestAgentWithTools(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/agent_with_tools.yml")

			logger := discardLogger()

			driver, err := df.new("test-agent-tools-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-tools", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			var captured agent.AgentConfig

			result := &agent.AgentResult{
				Text:   "Used tools",
				Status: "success",
			}

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{
				AgentRunFunc: mockAgentRunner(&captured, result),
			})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify tools were resolved.
			assert.Expect(captured.Tools).To(HaveLen(2))

			// Agent tool.
			assert.Expect(captured.Tools[0].Name).To(Equal("sub-agent"))
			assert.Expect(captured.Tools[0].Prompt).To(Equal("I am a sub-agent"))
			assert.Expect(captured.Tools[0].Model).To(Equal("test/sub-model"))
			assert.Expect(captured.Tools[0].IsTask).To(BeFalse())

			// Task tool.
			assert.Expect(captured.Tools[1].Name).To(Equal("my-tool"))
			assert.Expect(captured.Tools[1].IsTask).To(BeTrue())
			assert.Expect(captured.Tools[1].Description).To(Equal("A test tool"))
			assert.Expect(captured.Tools[1].CommandPath).To(Equal("echo"))
			assert.Expect(captured.Tools[1].CommandArgs).To(Equal([]string{"hello"}))
		})
	}
}

func TestAgentFailure(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/agent_basic.yml")
			// Remove assertion since job will fail.
			cfg.Jobs[0].Assert = nil

			logger := discardLogger()

			driver, err := df.new("test-agent-fail-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-fail", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{
				AgentRunFunc: mockAgentRunnerWithError("LLM provider unavailable"),
			})
			err = runner.Run(context.Background())
			assert.Expect(err).To(HaveOccurred())
			assert.Expect(err.Error()).To(ContainSubstring("test-agent"))

			// Verify storage has failure state.
			val, err := store.Get(context.Background(), "/pipeline/test-run/jobs/agent-test/1/run")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(val).To(HaveKeyWithValue("status", "failure"))
			assert.Expect(val).To(HaveKey("error_message"))
			assert.Expect(val).To(HaveKeyWithValue("stdout", "partial output"))
		})
	}
}

func TestAgentLimitExceeded(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			cfg := loadConfig(t, "steps/agent_basic.yml")

			logger := discardLogger()

			driver, err := df.new("test-agent-limit-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-limit", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			var captured agent.AgentConfig

			result := &agent.AgentResult{
				Text:   "Ran out of tokens",
				Status: "limit_exceeded",
				Usage: agent.AgentUsage{
					TotalTokens: 500000,
					LLMRequests: 20,
				},
			}

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{
				AgentRunFunc: mockAgentRunner(&captured, result),
			})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify storage has limit_exceeded status.
			val, err := store.Get(context.Background(), "/pipeline/test-run/jobs/agent-test/1/run")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(val).To(HaveKeyWithValue("status", "limit_exceeded"))
		})
	}
}

func TestResolveAgentImage(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	// Inline image takes priority.
	assert.Expect(backwards.ResolveAgentImage(&backwards.TestStep{Image: "golang:latest"})).To(Equal("golang:latest"))

	// Image resource fallback.
	assert.Expect(backwards.ResolveAgentImage(&backwards.TestStep{ImageResourceRepo: "alpine/git"})).To(Equal("alpine/git"))

	// Default busybox.
	assert.Expect(backwards.ResolveAgentImage(&backwards.TestStep{})).To(Equal("busybox"))
}

func TestMergeAgentFromContents(t *testing.T) {
	t.Parallel()

	t.Run("concatenates prompts", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		result := backwards.MergeAgentFromContents(
			[]byte("prompt: file prompt\nmodel: file-model"),
			&backwards.TestStep{Prompt: "inline prompt"},
		)
		assert.Expect(result.Prompt).To(Equal("file prompt\ninline prompt"))
		// Inline model should take priority (if set).
	})

	t.Run("uses file prompt when inline is empty", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		result := backwards.MergeAgentFromContents(
			[]byte("prompt: file prompt only"),
			&backwards.TestStep{},
		)
		assert.Expect(result.Prompt).To(Equal("file prompt only"))
	})

	t.Run("inherits model from file when not set inline", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		result := backwards.MergeAgentFromContents(
			[]byte("model: file-model\nprompt: test"),
			&backwards.TestStep{},
		)
		assert.Expect(result.Model).To(Equal("file-model"))
	})

	t.Run("inline model overrides file model", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		result := backwards.MergeAgentFromContents(
			[]byte("model: file-model\nprompt: test"),
			&backwards.TestStep{Model: "inline-model"},
		)
		assert.Expect(result.Model).To(Equal("inline-model"))
	})
}
