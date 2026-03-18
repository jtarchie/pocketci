package runner_test

import (
	"testing"

	"github.com/jtarchie/pocketci/runtime/runner"
	. "github.com/onsi/gomega"
)

func TestAppendLogEntry(t *testing.T) {
	t.Parallel()

	t.Run("appends new entry for empty slice", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		logs := runner.AppendLogEntry(nil, "stdout", "hello")
		assert.Expect(logs).To(Equal([]runner.TaskLogEntry{
			{Type: "stdout", Content: "hello"},
		}))
	})

	t.Run("condenses consecutive same-type entries", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var logs []runner.TaskLogEntry
		logs = runner.AppendLogEntry(logs, "stdout", "chunk1")
		logs = runner.AppendLogEntry(logs, "stdout", "chunk2")
		logs = runner.AppendLogEntry(logs, "stdout", "chunk3")

		assert.Expect(logs).To(HaveLen(1))
		assert.Expect(logs[0]).To(Equal(runner.TaskLogEntry{
			Type:    "stdout",
			Content: "chunk1chunk2chunk3",
		}))
	})

	t.Run("preserves separate entries for alternating types", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var logs []runner.TaskLogEntry
		logs = runner.AppendLogEntry(logs, "stdout", "out1")
		logs = runner.AppendLogEntry(logs, "stderr", "err1")
		logs = runner.AppendLogEntry(logs, "stdout", "out2")

		assert.Expect(logs).To(HaveLen(3))
		assert.Expect(logs[0]).To(Equal(runner.TaskLogEntry{Type: "stdout", Content: "out1"}))
		assert.Expect(logs[1]).To(Equal(runner.TaskLogEntry{Type: "stderr", Content: "err1"}))
		assert.Expect(logs[2]).To(Equal(runner.TaskLogEntry{Type: "stdout", Content: "out2"}))
	})

	t.Run("skips empty data", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var logs []runner.TaskLogEntry
		logs = runner.AppendLogEntry(logs, "stdout", "hello")
		logs = runner.AppendLogEntry(logs, "stdout", "")
		logs = runner.AppendLogEntry(logs, "stderr", "")

		assert.Expect(logs).To(HaveLen(1))
		assert.Expect(logs[0]).To(Equal(runner.TaskLogEntry{Type: "stdout", Content: "hello"}))
	})

	t.Run("condenses stderr followed by more stderr", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		var logs []runner.TaskLogEntry
		logs = runner.AppendLogEntry(logs, "stdout", "out")
		logs = runner.AppendLogEntry(logs, "stderr", "err1")
		logs = runner.AppendLogEntry(logs, "stderr", "err2")

		assert.Expect(logs).To(HaveLen(2))
		assert.Expect(logs[0]).To(Equal(runner.TaskLogEntry{Type: "stdout", Content: "out"}))
		assert.Expect(logs[1]).To(Equal(runner.TaskLogEntry{Type: "stderr", Content: "err1err2"}))
	})
}
