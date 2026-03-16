package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

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
	writeCmd := resultJsonWriteCmd(mountName, data)

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

func TestParseTaskStepID(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	for _, tc := range []struct {
		input    string
		wantIdx  int
		wantName string
	}{
		{"0-git-clone", 0, "git-clone"},
		{"12-run-tests", 12, "run-tests"},
		{"badid", -1, "badid"},
		{"x-name", -1, "x-name"},
	} {
		idx, name := parseTaskStepID(tc.input)
		assert.Expect(idx).To(Equal(tc.wantIdx), "idx for %q", tc.input)
		assert.Expect(name).To(Equal(tc.wantName), "name for %q", tc.input)
	}
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

func TestLevenshtein(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"BUILD", "build", 0}, // case-insensitive
	} {
		assert.Expect(levenshtein(tc.a, tc.b)).To(Equal(tc.want), "%q vs %q", tc.a, tc.b)
	}
}

func TestFuzzyFindTask(t *testing.T) {
	t.Parallel()

	tasks := []taskSummary{
		{Name: "git-clone", Index: 0, Status: "success"},
		{Name: "run-tests", Index: 1, Status: "failure"},
		{Name: "build", Index: 2, Status: "success"},
		{Name: "deploy", Index: 3, Status: "pending"},
	}

	t.Run("exact match", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		got, ok := fuzzyFindTask(tasks, "build")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("build"))
		assert.Expect(got.Index).To(Equal(2))
	})

	t.Run("partial substring match", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		got, ok := fuzzyFindTask(tasks, "test")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("run-tests"))
	})

	t.Run("case-insensitive substring", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		got, ok := fuzzyFindTask(tasks, "GIT")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("git-clone"))
	})

	t.Run("fuzzy fallback picks closest", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		// "deploi" is closest in edit distance to "deploy".
		got, ok := fuzzyFindTask(tasks, "deploi")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("deploy"))
	})

	t.Run("empty task list returns false", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		_, ok := fuzzyFindTask(nil, "build")
		assert.Expect(ok).To(BeFalse())
	})
}

func TestTruncateStr(t *testing.T) {
	t.Parallel()

	t.Run("no truncation when shorter", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		s, truncated := truncateStr("hello", 10)
		assert.Expect(s).To(Equal("hello"))
		assert.Expect(truncated).To(BeFalse())
	})

	t.Run("truncates when longer", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		s, truncated := truncateStr("hello world", 5)
		assert.Expect(s).To(Equal("hello"))
		assert.Expect(truncated).To(BeTrue())
	})

	t.Run("zero maxBytes means no truncation", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		s, truncated := truncateStr("hello", 0)
		assert.Expect(s).To(Equal("hello"))
		assert.Expect(truncated).To(BeFalse())
	})
}

func TestLoadTaskSummaries_Sorting(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tasks := []taskSummary{
		{Name: "build", Index: 2},
		{Name: "clone", Index: 0},
		{Name: "test", Index: 1},
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].Index < tasks[j].Index
	})

	assert.Expect(tasks[0].Name).To(Equal("clone"))
	assert.Expect(tasks[1].Name).To(Equal("test"))
	assert.Expect(tasks[2].Name).To(Equal("build"))
}

func TestTaskSummaryToMap(t *testing.T) {
	t.Parallel()

	t.Run("all fields present", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		ts := taskSummary{
			Name:      "build",
			Index:     3,
			Status:    "success",
			StartedAt: "2026-01-01T00:00:00Z",
			Elapsed:   "5s",
		}
		m := taskSummaryToMap(ts)
		assert.Expect(m["name"]).To(Equal("build"))
		assert.Expect(m["index"]).To(Equal(3))
		assert.Expect(m["status"]).To(Equal("success"))
		assert.Expect(m["started_at"]).To(Equal("2026-01-01T00:00:00Z"))
		assert.Expect(m["elapsed"]).To(Equal("5s"))
	})

	t.Run("empty optional fields omitted", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		ts := taskSummary{Name: "build", Index: 0}
		m := taskSummaryToMap(ts)
		_, hasStartedAt := m["started_at"]
		_, hasElapsed := m["elapsed"]
		assert.Expect(hasStartedAt).To(BeFalse())
		assert.Expect(hasElapsed).To(BeFalse())
	})
}

func TestParseTaskSummaryPath(t *testing.T) {
	t.Parallel()

	t.Run("supports legacy tasks layout", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := parseTaskSummaryPath("/pipeline/run-1/tasks/2-build")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(2))
		assert.Expect(name).To(Equal("build"))
	})

	t.Run("supports backwards job agent layout", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := parseTaskSummaryPath("/pipeline/run-1/jobs/review-pr/4/agent/final-reviewer")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(4))
		assert.Expect(name).To(Equal("final-reviewer"))
	})

	t.Run("supports backwards job task layout", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := parseTaskSummaryPath("/pipeline/run-1/jobs/review-pr/0/tasks/clone-pr")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(0))
		assert.Expect(name).To(Equal("clone-pr"))
	})

	t.Run("supports backwards job task layout with attempt suffix", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := parseTaskSummaryPath("/pipeline/run-1/jobs/review-pr/5/tasks/post-comment/attempt/2")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(5))
		assert.Expect(name).To(Equal("post-comment"))
	})

	t.Run("ignores non-task job paths", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		_, _, ok := parseTaskSummaryPath("/pipeline/run-1/jobs/review-pr")
		assert.Expect(ok).To(BeFalse())
	})
}

func TestResolveOutputMountPath(t *testing.T) {
	t.Parallel()

	config := AgentConfig{
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
		resolved := resolveOutputMountPath(config)
		assert.Expect(resolved).To(Equal("final-review"))
	})

	t.Run("keeps mount path if already mount name", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		cfg := config
		cfg.OutputVolumePath = "final-review"
		resolved := resolveOutputMountPath(cfg)
		assert.Expect(resolved).To(Equal("final-review"))
	})
}

func TestNormalizeContextGuardConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil config disables guard", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := normalizeContextGuardConfig(nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal(""))
		assert.Expect(value).To(Equal(0))
	})

	t.Run("sliding window uses explicit max turns", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := normalizeContextGuardConfig(&AgentContextGuardConfig{
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
		strategy, value, err := normalizeContextGuardConfig(&AgentContextGuardConfig{Strategy: "sliding_window"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("sliding_window"))
		assert.Expect(value).To(Equal(defaultContextGuardMaxTurns))
	})

	t.Run("threshold uses explicit max tokens", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := normalizeContextGuardConfig(&AgentContextGuardConfig{
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
		strategy, value, err := normalizeContextGuardConfig(&AgentContextGuardConfig{Strategy: "threshold"})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("threshold"))
		assert.Expect(value).To(Equal(defaultContextGuardMaxTokens))
	})

	t.Run("missing strategy infers sliding window from max turns", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := normalizeContextGuardConfig(&AgentContextGuardConfig{MaxTurns: 7})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("sliding_window"))
		assert.Expect(value).To(Equal(7))
	})

	t.Run("missing strategy defaults to threshold", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		strategy, value, err := normalizeContextGuardConfig(&AgentContextGuardConfig{})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strategy).To(Equal("threshold"))
		assert.Expect(value).To(Equal(defaultContextGuardMaxTokens))
	})

	t.Run("invalid strategy returns an error", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		_, _, err := normalizeContextGuardConfig(&AgentContextGuardConfig{Strategy: "weird"})
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("invalid context_guard strategy"))
	})
}

func TestEffectiveLimits(t *testing.T) {
	t.Parallel()

	t.Run("nil config uses default max turns with no token limit", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := effectiveLimits(nil)
		assert.Expect(turns).To(Equal(defaultLimitsMaxTurns))
		assert.Expect(tokens).To(Equal(int32(0)))
	})

	t.Run("explicit max turns is used", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := effectiveLimits(&AgentLimitsConfig{MaxTurns: 10})
		assert.Expect(turns).To(Equal(10))
		assert.Expect(tokens).To(Equal(int32(0)))
	})

	t.Run("zero max turns falls back to default", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, _ := effectiveLimits(&AgentLimitsConfig{MaxTurns: 0})
		assert.Expect(turns).To(Equal(defaultLimitsMaxTurns))
	})

	t.Run("explicit max total tokens is used", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := effectiveLimits(&AgentLimitsConfig{MaxTurns: 5, MaxTotalTokens: 100000})
		assert.Expect(turns).To(Equal(5))
		assert.Expect(tokens).To(Equal(int32(100000)))
	})

	t.Run("empty config uses defaults", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		turns, tokens := effectiveLimits(&AgentLimitsConfig{})
		assert.Expect(turns).To(Equal(defaultLimitsMaxTurns))
		assert.Expect(tokens).To(Equal(int32(0)))
	})
}
