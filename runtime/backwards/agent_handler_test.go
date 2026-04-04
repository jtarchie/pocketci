package backwards_test

import (
	"context"
	"testing"

	"github.com/jtarchie/pocketci/runtime/agent"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestAgentBasic(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			configureFakeLLM(t, fakeLLMResponse("I completed the task.", 10, 5, 15))

			cfg := loadConfig(t, "steps/agent_basic.yml")

			logger := discardLogger()

			driver, err := df.new("test-agent-basic-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-basic", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify storage has success state.
			val, err := store.Get(context.Background(), "/pipeline/test-run/jobs/agent-test/1/run")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(val).To(HaveKeyWithValue("status", "success"))
			assert.Expect(val).To(HaveKey("elapsed"))
			assert.Expect(val).To(HaveKeyWithValue("stdout", "I completed the task."))

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

			configureFakeLLM(t, fakeLLMResponse("Read the data successfully", 10, 5, 15))

			cfg := loadConfig(t, "steps/agent_with_volumes.yml")

			logger := discardLogger()

			driver, err := df.new("test-agent-vols-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-vols", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			val, err := store.Get(context.Background(), "/pipeline/test-run/jobs/volume-test/1/run")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(val).To(HaveKeyWithValue("status", "success"))
		})
	}
}

func TestAgentWithTools(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			var capturedBody string
			configureFakeLLMWithCapture(t, fakeLLMResponse("Used tools", 10, 5, 15), &capturedBody)

			cfg := loadConfig(t, "steps/agent_with_tools.yml")

			logger := discardLogger()

			driver, err := df.new("test-agent-tools-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-tools", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).NotTo(HaveOccurred())

			// Verify tools were sent to the LLM.
			assert.Expect(capturedBody).To(ContainSubstring("sub-agent"))
			assert.Expect(capturedBody).To(ContainSubstring("my-tool"))
		})
	}
}

func TestAgentFailure(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			configureFakeLLMError(t)

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

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{})
			err = runner.Run(context.Background())
			assert.Expect(err).To(HaveOccurred())
			assert.Expect(err.Error()).To(ContainSubstring("test-agent"))

			// Verify storage has failure state.
			val, err := store.Get(context.Background(), "/pipeline/test-run/jobs/agent-test/1/run")
			assert.Expect(err).NotTo(HaveOccurred())
			assert.Expect(val).To(HaveKeyWithValue("status", "failure"))
			assert.Expect(val).To(HaveKey("error_message"))
		})
	}
}

func TestAgentLimitExceeded(t *testing.T) {
	for _, df := range drivers {
		t.Run(df.name, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			// Large token counts to trigger limit with MaxTotalTokens: 1.
			configureFakeLLM(t, fakeLLMResponse("Ran out of tokens", 100, 100, 200))

			cfg := loadConfig(t, "steps/agent_basic.yml")
			cfg.Jobs[0].Plan[1].AgentLimits = &agent.AgentLimitsConfig{MaxTotalTokens: 1}

			logger := discardLogger()

			driver, err := df.new("test-agent-limit-"+df.name, logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = driver.Close() }()

			store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-agent-limit", logger)
			assert.Expect(err).NotTo(HaveOccurred())

			defer func() { _ = store.Close() }()

			runner := backwards.New(cfg, driver, store, logger, "test-run", "test-pipeline", backwards.RunnerOptions{})
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
