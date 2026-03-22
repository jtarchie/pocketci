package agent_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/runtime/agent"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"

	. "github.com/onsi/gomega"
)

// TestResultJsonWriteCmd_StdinDependency demonstrates that the current
// "cat > result.json" approach produces an empty file when stdin is not
// piped — which is what happens on the Fly driver where stdin does not
// survive nested sh -c invocations.
func TestResultJsonWriteCmd_StdinDependency(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tmpDir := t.TempDir()
	mountName := "final-review"

	data, err := json.Marshal(map[string]string{
		"status": "success",
		"text":   "Review with $special `chars` and 'quotes' and \"doubles\".",
	})
	assert.Expect(err).NotTo(HaveOccurred())

	// This is the exact command from RunAgent — it depends on stdin.
	writeCmd := agent.ResultJsonWriteCmd(mountName, data)

	cmd := exec.Command("sh", "-c", writeCmd) //nolint:gosec
	cmd.Dir = tmpDir
	// Intentionally do NOT set cmd.Stdin — simulates Fly exec behaviour
	// where stdin is not piped through nested shell invocations.
	out, _ := cmd.CombinedOutput()
	_ = out

	content, readErr := os.ReadFile(filepath.Join(tmpDir, mountName, "result.json"))
	assert.Expect(readErr).NotTo(HaveOccurred())

	// The file must contain the full JSON — not be empty.
	assert.Expect(strings.TrimSpace(string(content))).NotTo(BeEmpty(),
		"result.json was empty because stdin was not piped through the shell")

	var result map[string]string
	assert.Expect(json.Unmarshal(content, &result)).To(Succeed())
	assert.Expect(result["status"]).To(Equal("success"))
	assert.Expect(result["text"]).To(ContainSubstring("$special"))
	assert.Expect(result["text"]).To(ContainSubstring("`chars`"))
	assert.Expect(result["text"]).To(ContainSubstring("'quotes'"))
}

func TestRunScript_ShellBehavior(t *testing.T) {
	t.Parallel()

	t.Run("multi_step", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := "set -e\necho hello\necho world"
		cmd := exec.Command("/bin/sh", "-c", script) //nolint:gosec
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(out)).To(ContainSubstring("hello"))
		assert.Expect(string(out)).To(ContainSubstring("world"))
	})

	t.Run("fail_fast", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := "set -e\nfalse\necho should_not_appear"
		cmd := exec.Command("/bin/sh", "-c", script) //nolint:gosec
		out, err := cmd.CombinedOutput()
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(string(out)).NotTo(ContainSubstring("should_not_appear"))
	})
}

func TestResolveOutputMountPath(t *testing.T) {
	t.Parallel()

	config := agent.AgentConfig{
		OutputVolumePath: "/workspace/volumes/out",
		Mounts: map[string]pipelinerunner.VolumeResult{
			"final-review": {
				Name: "vol-final-review",
				Path: "/workspace/volumes/out",
			},
		},
	}

	t.Run("resolves host path to mount name", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		resolved := agent.ResolveOutputMountPath(config)
		assert.Expect(resolved).To(Equal("final-review"))
	})

	t.Run("keeps mount path if already mount name", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		cfg := config
		cfg.OutputVolumePath = "final-review"
		resolved := agent.ResolveOutputMountPath(cfg)
		assert.Expect(resolved).To(Equal("final-review"))
	})
}

func TestNormalizeContextGuardConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil config disables guard", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := agent.NormalizeContextGuardConfig(nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal(""))
		assert.Expect(value).To(Equal(0))
	})

	t.Run("sliding window uses explicit max turns", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := agent.NormalizeContextGuardConfig(&agent.AgentContextGuardConfig{
			Strategy: "sliding_window",
			MaxTurns: 12,
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("sliding_window"))
		assert.Expect(value).To(Equal(12))
	})

	t.Run("sliding window falls back to default turns", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := agent.NormalizeContextGuardConfig(&agent.AgentContextGuardConfig{Strategy: "sliding_window"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("sliding_window"))
		assert.Expect(value).To(Equal(agent.DefaultContextGuardMaxTurns))
	})

	t.Run("threshold uses explicit max tokens", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := agent.NormalizeContextGuardConfig(&agent.AgentContextGuardConfig{
			Strategy:  "threshold",
			MaxTokens: 64000,
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("threshold"))
		assert.Expect(value).To(Equal(64000))
	})

	t.Run("threshold falls back to default tokens", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := agent.NormalizeContextGuardConfig(&agent.AgentContextGuardConfig{Strategy: "threshold"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("threshold"))
		assert.Expect(value).To(Equal(agent.DefaultContextGuardMaxTokens))
	})

	t.Run("missing strategy infers sliding window from max turns", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := agent.NormalizeContextGuardConfig(&agent.AgentContextGuardConfig{MaxTurns: 7})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("sliding_window"))
		assert.Expect(value).To(Equal(7))
	})

	t.Run("missing strategy defaults to threshold", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := agent.NormalizeContextGuardConfig(&agent.AgentContextGuardConfig{})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("threshold"))
		assert.Expect(value).To(Equal(agent.DefaultContextGuardMaxTokens))
	})

	t.Run("invalid strategy returns an error", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		_, _, err := agent.NormalizeContextGuardConfig(&agent.AgentContextGuardConfig{Strategy: "weird"})
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("invalid context_guard strategy"))
	})
}

func TestEffectiveLimits(t *testing.T) {
	t.Parallel()

	t.Run("nil config uses default max turns with no token limit", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := agent.EffectiveLimits(nil)
		assert.Expect(turns).To(Equal(agent.DefaultLimitsMaxTurns))
		assert.Expect(tokens).To(Equal(int32(0)))
	})

	t.Run("explicit max turns is used", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := agent.EffectiveLimits(&agent.AgentLimitsConfig{MaxTurns: 10})
		assert.Expect(turns).To(Equal(10))
		assert.Expect(tokens).To(Equal(int32(0)))
	})

	t.Run("zero max turns falls back to default", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, _ := agent.EffectiveLimits(&agent.AgentLimitsConfig{MaxTurns: 0})
		assert.Expect(turns).To(Equal(agent.DefaultLimitsMaxTurns))
	})

	t.Run("explicit max total tokens is used", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := agent.EffectiveLimits(&agent.AgentLimitsConfig{MaxTurns: 5, MaxTotalTokens: 100000})
		assert.Expect(turns).To(Equal(5))
		assert.Expect(tokens).To(Equal(int32(100000)))
	})

	t.Run("empty config uses defaults", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := agent.EffectiveLimits(&agent.AgentLimitsConfig{})
		assert.Expect(turns).To(Equal(agent.DefaultLimitsMaxTurns))
		assert.Expect(tokens).To(Equal(int32(0)))
	})
}
