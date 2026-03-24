package agent_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

func TestReadFile_OffsetLimit(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a 10-line file.
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line %d content", i))
	}

	err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("default reads from beginning with line numbers", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		// Simulate the awk command the tool generates (offset=1, limit=2000).
		script := fmt.Sprintf("awk 'NR>=1 && NR<=2000 {printf \"%%6d\\t%%s\\n\", NR, $0}' %s/test.txt", tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())

		output := string(out)
		assert.Expect(output).To(ContainSubstring("     1\tline 1 content"))
		assert.Expect(output).To(ContainSubstring("    10\tline 10 content"))
	})

	t.Run("offset and limit extract correct range", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		offset := 3
		limit := 4
		end := offset + limit - 1

		script := fmt.Sprintf("awk 'NR>=%d && NR<=%d {printf \"%%6d\\t%%s\\n\", NR, $0}' %s/test.txt", offset, end, tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())

		output := string(out)
		outputLines := strings.Split(strings.TrimRight(output, "\n"), "\n")
		assert.Expect(outputLines).To(HaveLen(4))
		assert.Expect(output).To(ContainSubstring("     3\tline 3 content"))
		assert.Expect(output).To(ContainSubstring("     6\tline 6 content"))
		assert.Expect(output).NotTo(ContainSubstring("line 2 content"))
		assert.Expect(output).NotTo(ContainSubstring("line 7 content"))
	})
}

func TestGrep_ShellBehavior(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create test files.
	err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filepath.Join(tmpDir, "src", "main.go"), []byte("package main\nfunc main() {}\nfunc helper() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "src", "util.go"), []byte("package main\nfunc helper() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("# README\nNo functions here.\n"), 0o644)

	t.Run("finds matches with line numbers", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := fmt.Sprintf("grep -rn 'func' %s | head -n 100", tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())

		output := string(out)
		assert.Expect(output).To(ContainSubstring("func main()"))
		assert.Expect(output).To(ContainSubstring("func helper()"))
	})

	t.Run("glob filter restricts to matching files", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := fmt.Sprintf("grep -rn --include='*.go' 'func' %s | head -n 100", tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())

		output := string(out)
		assert.Expect(output).To(ContainSubstring("func"))
		assert.Expect(output).NotTo(ContainSubstring("readme.md"))
	})

	t.Run("case insensitive search", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := fmt.Sprintf("grep -rn -i 'FUNC' %s | head -n 100", tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(out)).To(ContainSubstring("func main()"))
	})

	t.Run("no matches returns empty", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := fmt.Sprintf("grep -rn 'nonexistent_xyz' %s | head -n 100", tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, _ := cmd.CombinedOutput()
		assert.Expect(strings.TrimSpace(string(out))).To(BeEmpty())
	})
}

func TestGlob_BuildFindCommand(t *testing.T) {
	t.Parallel()

	t.Run("recursive glob **/*.ext", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		cmd := agent.BuildFindCommand("**/*.go", ".")
		assert.Expect(cmd).To(ContainSubstring("find . -name '*.go' -type f"))
	})

	t.Run("dir-scoped glob dir/**/*.ext", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		cmd := agent.BuildFindCommand("src/**/*.go", ".")
		assert.Expect(cmd).To(ContainSubstring("find ./src -name '*.go' -type f"))
	})

	t.Run("shallow glob *.ext uses maxdepth", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		cmd := agent.BuildFindCommand("*.yml", ".")
		assert.Expect(cmd).To(ContainSubstring("-maxdepth 1"))
		assert.Expect(cmd).To(ContainSubstring("-name '*.yml'"))
	})

	t.Run("fallback uses -path", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		cmd := agent.BuildFindCommand("config/settings", ".")
		assert.Expect(cmd).To(ContainSubstring("-path '*config/settings*'"))
	})
}

func TestGlob_FindExecution(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(tmpDir, "src", "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(tmpDir, "src", "main.go"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "src", "sub", "util.go"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte(""), 0o644)

	t.Run("finds go files recursively", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := agent.BuildFindCommand("**/*.go", tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())

		output := string(out)
		assert.Expect(output).To(ContainSubstring("main.go"))
		assert.Expect(output).To(ContainSubstring("util.go"))
		assert.Expect(output).NotTo(ContainSubstring("readme.md"))
	})

	t.Run("no matches returns empty", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		script := agent.BuildFindCommand("**/*.rs", tmpDir)
		cmd := exec.Command("sh", "-c", script)
		out, err := cmd.Output()
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(strings.TrimSpace(string(out))).To(BeEmpty())
	})
}

func TestWriteFile_Base64(t *testing.T) {
	t.Parallel()

	t.Run("writes content via base64", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "output.txt")
		content := "Hello, world!\nLine 2\n"
		encoded := base64.StdEncoding.EncodeToString([]byte(content))

		script := fmt.Sprintf("mkdir -p \"$(dirname '%s')\" && printf '%%s' '%s' | base64 -d > '%s'",
			filePath, encoded, filePath)
		cmd := exec.Command("sh", "-c", script)
		_, err := cmd.CombinedOutput()
		assert.Expect(err).NotTo(HaveOccurred())

		got, err := os.ReadFile(filePath)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(got)).To(Equal(content))
	})

	t.Run("creates parent directories", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "a", "b", "c", "deep.txt")
		content := "deep content"
		encoded := base64.StdEncoding.EncodeToString([]byte(content))

		script := fmt.Sprintf("mkdir -p \"$(dirname '%s')\" && printf '%%s' '%s' | base64 -d > '%s'",
			filePath, encoded, filePath)
		cmd := exec.Command("sh", "-c", script)
		_, err := cmd.CombinedOutput()
		assert.Expect(err).NotTo(HaveOccurred())

		got, err := os.ReadFile(filePath)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(got)).To(Equal(content))
	})

	t.Run("handles special characters", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "special.txt")
		content := "has $dollars, `backticks`, 'quotes', \"doubles\", and\nnewlines\n"
		encoded := base64.StdEncoding.EncodeToString([]byte(content))

		script := fmt.Sprintf("mkdir -p \"$(dirname '%s')\" && printf '%%s' '%s' | base64 -d > '%s'",
			filePath, encoded, filePath)
		cmd := exec.Command("sh", "-c", script)
		_, err := cmd.CombinedOutput()
		assert.Expect(err).NotTo(HaveOccurred())

		got, err := os.ReadFile(filePath)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(string(got)).To(Equal(content))
	})
}

func TestShellJoinArgs(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	t.Run("flags pass through unquoted", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		result := agent.ShellJoinArgs([]string{"-rn", "-i", "--", "pattern", "path"})
		assert.Expect(result).To(Equal("-rn -i -- 'pattern' 'path'"))
	})

	t.Run("quotes special characters", func(t *testing.T) {
		t.Parallel()

		result := agent.ShellJoinArgs([]string{"--", "hello world", "it's"})
		assert.Expect(result).To(Equal("-- 'hello world' 'it'\\''s'"))
	})
}
