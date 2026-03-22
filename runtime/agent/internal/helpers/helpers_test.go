package helpers_test

import (
	"sort"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/runtime/agent/internal/helpers"
)

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	assert.Expect(helpers.FormatDuration(0)).To(Equal("0s"))
	assert.Expect(helpers.FormatDuration(5 * time.Second)).To(Equal("5s"))
	assert.Expect(helpers.FormatDuration(59 * time.Second)).To(Equal("59s"))
	assert.Expect(helpers.FormatDuration(60 * time.Second)).To(Equal("1m 0s"))
	assert.Expect(helpers.FormatDuration(90 * time.Second)).To(Equal("1m 30s"))
	assert.Expect(helpers.FormatDuration(3661 * time.Second)).To(Equal("1h 1m 1s"))
	assert.Expect(helpers.FormatDuration(7200 * time.Second)).To(Equal("2h 0m 0s"))
}

func TestFuzzyFindTask(t *testing.T) {
	t.Parallel()

	tasks := []helpers.TaskSummary{
		{Name: "git-clone", Index: 0, Status: "success"},
		{Name: "run-tests", Index: 1, Status: "failure"},
		{Name: "build", Index: 2, Status: "success"},
		{Name: "deploy", Index: 3, Status: "pending"},
	}

	t.Run("exact match", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		got, ok := helpers.FuzzyFindTask(tasks, "build")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("build"))
		assert.Expect(got.Index).To(Equal(2))
	})

	t.Run("partial substring match", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		got, ok := helpers.FuzzyFindTask(tasks, "test")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("run-tests"))
	})

	t.Run("case-insensitive substring", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		got, ok := helpers.FuzzyFindTask(tasks, "GIT")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("git-clone"))
	})

	t.Run("fuzzy fallback picks closest", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		// "deploi" is closest in edit distance to "deploy".
		got, ok := helpers.FuzzyFindTask(tasks, "deploi")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(got.Name).To(Equal("deploy"))
	})

	t.Run("empty task list returns false", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		_, ok := helpers.FuzzyFindTask(nil, "build")
		assert.Expect(ok).To(BeFalse())
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
		assert.Expect(helpers.Levenshtein(tc.a, tc.b)).To(Equal(tc.want), "%q vs %q", tc.a, tc.b)
	}
}

func TestLoadTaskSummaries_Sorting(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	tasks := []helpers.TaskSummary{
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
		idx, name := helpers.ParseTaskStepID(tc.input)
		assert.Expect(idx).To(Equal(tc.wantIdx), "idx for %q", tc.input)
		assert.Expect(name).To(Equal(tc.wantName), "name for %q", tc.input)
	}
}

func TestParseTaskSummaryPath(t *testing.T) {
	t.Parallel()

	t.Run("supports legacy tasks layout", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := helpers.ParseTaskSummaryPath("/pipeline/run-1/tasks/2-build")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(2))
		assert.Expect(name).To(Equal("build"))
	})

	t.Run("supports backwards job agent layout", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := helpers.ParseTaskSummaryPath("/pipeline/run-1/jobs/review-pr/4/agent/final-reviewer")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(4))
		assert.Expect(name).To(Equal("final-reviewer"))
	})

	t.Run("supports backwards job task layout", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := helpers.ParseTaskSummaryPath("/pipeline/run-1/jobs/review-pr/0/tasks/clone-pr")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(0))
		assert.Expect(name).To(Equal("clone-pr"))
	})

	t.Run("supports backwards job task layout with attempt suffix", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		idx, name, ok := helpers.ParseTaskSummaryPath("/pipeline/run-1/jobs/review-pr/5/tasks/post-comment/attempt/2")
		assert.Expect(ok).To(BeTrue())
		assert.Expect(idx).To(Equal(5))
		assert.Expect(name).To(Equal("post-comment"))
	})

	t.Run("ignores non-task job paths", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		_, _, ok := helpers.ParseTaskSummaryPath("/pipeline/run-1/jobs/review-pr")
		assert.Expect(ok).To(BeFalse())
	})
}

func TestTaskSummaryToMap(t *testing.T) {
	t.Parallel()

	t.Run("all fields present", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		ts := helpers.TaskSummary{
			Name:      "build",
			Index:     3,
			Status:    "success",
			StartedAt: "2026-01-01T00:00:00Z",
			Elapsed:   "5s",
		}
		m := helpers.TaskSummaryToMap(ts)
		assert.Expect(m["name"]).To(Equal("build"))
		assert.Expect(m["index"]).To(Equal(3))
		assert.Expect(m["status"]).To(Equal("success"))
		assert.Expect(m["started_at"]).To(Equal("2026-01-01T00:00:00Z"))
		assert.Expect(m["elapsed"]).To(Equal("5s"))
	})

	t.Run("empty optional fields omitted", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		ts := helpers.TaskSummary{Name: "build", Index: 0}
		m := helpers.TaskSummaryToMap(ts)
		_, hasStartedAt := m["started_at"]
		_, hasElapsed := m["elapsed"]
		assert.Expect(hasStartedAt).To(BeFalse())
		assert.Expect(hasElapsed).To(BeFalse())
	})
}

func TestTruncateStr(t *testing.T) {
	t.Parallel()

	t.Run("no truncation when shorter", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		s, truncated := helpers.TruncateStr("hello", 10)
		assert.Expect(s).To(Equal("hello"))
		assert.Expect(truncated).To(BeFalse())
	})

	t.Run("truncates when longer", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		s, truncated := helpers.TruncateStr("hello world", 5)
		assert.Expect(s).To(Equal("hello"))
		assert.Expect(truncated).To(BeTrue())
	})

	t.Run("zero maxBytes means no truncation", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)
		s, truncated := helpers.TruncateStr("hello", 0)
		assert.Expect(s).To(Equal("hello"))
		assert.Expect(truncated).To(BeFalse())
	})
}
