package server_test

import (
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/server"
	. "github.com/onsi/gomega"
)

func TestWrapTerminalLines(t *testing.T) {
	t.Parallel()

	t.Run("empty input returns empty string", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		assert.Expect(server.WrapTerminalLines("", "test")).To(Equal(""))
	})

	t.Run("single line", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		result := server.WrapTerminalLines("hello world", "task1")
		assert.Expect(result).To(ContainSubstring(`id="task1-L1"`))
		assert.Expect(result).To(ContainSubstring(`href="#task1-L1"`))
		assert.Expect(result).To(ContainSubstring(`<span class="term-line-content">hello world</span>`))
		assert.Expect(strings.Count(result, `class="term-line"`)).To(Equal(1))
	})

	t.Run("multi-line plain text", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		result := server.WrapTerminalLines("line1\nline2\nline3", "t")
		assert.Expect(strings.Count(result, `class="term-line"`)).To(Equal(3))
		assert.Expect(result).To(ContainSubstring(`id="t-L1"`))
		assert.Expect(result).To(ContainSubstring(`id="t-L2"`))
		assert.Expect(result).To(ContainSubstring(`id="t-L3"`))
		assert.Expect(result).To(ContainSubstring(`<span class="term-line-content">line2</span>`))
	})

	t.Run("preserves HTML spans from ANSI rendering", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		html := `<span class="term-fg31">error</span>` + "\n" + `<span class="term-fg32">ok</span>`
		result := server.WrapTerminalLines(html, "ansi")
		assert.Expect(strings.Count(result, `class="term-line"`)).To(Equal(2))
		assert.Expect(result).To(ContainSubstring(`<span class="term-line-content"><span class="term-fg31">error</span></span>`))
		assert.Expect(result).To(ContainSubstring(`<span class="term-line-content"><span class="term-fg32">ok</span></span>`))
	})

	t.Run("line numbers are 1-indexed", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		result := server.WrapTerminalLines("a\nb", "x")
		assert.Expect(result).To(ContainSubstring(`>1</a>`))
		assert.Expect(result).To(ContainSubstring(`>2</a>`))
		assert.Expect(result).NotTo(ContainSubstring(`>0</a>`))
	})
}

func TestSanitizeTerminalID(t *testing.T) {
	t.Parallel()

	t.Run("replaces slashes with underscores", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		assert.Expect(server.SanitizeTerminalID("pipeline/abc/task/build")).To(Equal("pipeline_abc_task_build"))
	})

	t.Run("strips leading underscores from leading slashes", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		assert.Expect(server.SanitizeTerminalID("/pipeline/abc")).To(Equal("pipeline_abc"))
	})

	t.Run("handles empty string", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		assert.Expect(server.SanitizeTerminalID("")).To(Equal(""))
	})

	t.Run("no slashes returns as-is", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		assert.Expect(server.SanitizeTerminalID("simple")).To(Equal("simple"))
	})
}
