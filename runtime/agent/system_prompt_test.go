package agent_test

import (
	"testing"

	"github.com/jtarchie/pocketci/runtime/agent"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"

	. "github.com/onsi/gomega"
)

func TestBuildSystemInstruction(t *testing.T) {
	t.Parallel()

	t.Run("minimal config includes identity and core sections", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		result := agent.BuildSystemInstruction(agent.AgentConfig{}, 50)

		assert.Expect(result).To(ContainSubstring("You are an AI agent operating inside a CI/CD pipeline run."))
		assert.Expect(result).To(ContainSubstring("<tools>"))
		assert.Expect(result).To(ContainSubstring("</tools>"))
		assert.Expect(result).To(ContainSubstring("<efficiency>"))
		assert.Expect(result).To(ContainSubstring("</efficiency>"))
		assert.Expect(result).NotTo(ContainSubstring("<environment>"))
		assert.Expect(result).NotTo(ContainSubstring("<volumes>"))
		assert.Expect(result).NotTo(ContainSubstring("<output_format>"))
	})

	t.Run("environment section wraps metadata", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		result := agent.BuildSystemInstruction(agent.AgentConfig{
			Image:       "alpine/git",
			RunID:       "run-123",
			PipelineID:  "pipe-456",
			TriggeredBy: "webhook",
		}, 50)

		assert.Expect(result).To(ContainSubstring("<environment>"))
		assert.Expect(result).To(ContainSubstring("Container image: alpine/git"))
		assert.Expect(result).To(ContainSubstring("Pipeline run ID: run-123"))
		assert.Expect(result).To(ContainSubstring("Pipeline ID: pipe-456"))
		assert.Expect(result).To(ContainSubstring("Triggered by: webhook"))
		assert.Expect(result).To(ContainSubstring("</environment>"))
	})

	t.Run("volumes section lists mounts", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		result := agent.BuildSystemInstruction(agent.AgentConfig{
			Mounts: map[string]pipelinerunner.VolumeResult{
				"repo": {},
				"diff": {},
			},
		}, 50)

		assert.Expect(result).To(ContainSubstring("<volumes>"))
		assert.Expect(result).To(ContainSubstring("- repo/"))
		assert.Expect(result).To(ContainSubstring("- diff/"))
		assert.Expect(result).To(ContainSubstring("</volumes>"))
	})

	t.Run("tools section contains selection guidance", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		result := agent.BuildSystemInstruction(agent.AgentConfig{}, 50)

		assert.Expect(result).To(ContainSubstring("<tools>"))
		assert.Expect(result).To(ContainSubstring("read_file instead of cat"))
		assert.Expect(result).To(ContainSubstring("grep instead of shell grep"))
		assert.Expect(result).To(ContainSubstring("glob instead of shell find"))
		assert.Expect(result).To(ContainSubstring("write_file instead of shell redirection"))
		assert.Expect(result).To(ContainSubstring("run_script only for commands"))
		assert.Expect(result).To(ContainSubstring("</tools>"))
	})

	t.Run("efficiency section includes turn budget", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		result := agent.BuildSystemInstruction(agent.AgentConfig{}, 30)

		assert.Expect(result).To(ContainSubstring("<efficiency>"))
		assert.Expect(result).To(ContainSubstring("Budget: 30 turns."))
		assert.Expect(result).To(ContainSubstring("</efficiency>"))
	})

	t.Run("output_format section appears only with output schema", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		withoutSchema := agent.BuildSystemInstruction(agent.AgentConfig{}, 50)
		assert.Expect(withoutSchema).NotTo(ContainSubstring("<output_format>"))

		withSchema := agent.BuildSystemInstruction(agent.AgentConfig{
			OutputSchema: map[string]any{
				"summary": "string",
			},
		}, 50)
		assert.Expect(withSchema).To(ContainSubstring("<output_format>"))
		assert.Expect(withSchema).To(ContainSubstring("valid JSON"))
		assert.Expect(withSchema).To(ContainSubstring("</output_format>"))
	})
}
