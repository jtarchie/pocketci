package helpers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TaskSummary is the list_tasks tool output element.
type TaskSummary struct {
	Name      string `json:"name"`
	Index     int    `json:"index"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`
	Key       string `json:"-"`
}

// FormatDuration formats a duration as "Xs", "Xm Ys", or "Xh Ym Zs",
// matching the TS formatElapsed helper used by task and agent steps.
func FormatDuration(d time.Duration) string {
	totalSeconds := int(d.Seconds())
	h := totalSeconds / 3600
	m := (totalSeconds % 3600) / 60
	s := totalSeconds % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}

	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}

	return fmt.Sprintf("%ds", s)
}

// TruncateStr shortens s to at most maxBytes bytes. Returns the (possibly
// truncated) string and a flag indicating whether truncation occurred.
func TruncateStr(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}

	return s[:maxBytes], true
}

// Levenshtein computes the edit distance between two strings (case-insensitive).
func Levenshtein(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)

	if len(a) == 0 {
		return len(b)
	}

	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := range prev {
		prev[j] = j
	}

	for i, ca := range a {
		curr[0] = i + 1

		for j, cb := range b {
			cost := 1
			if ca == cb {
				cost = 0
			}

			curr[j+1] = min(curr[j]+1, min(prev[j+1]+1, prev[j]+cost))
		}

		prev, curr = curr, prev
	}

	return prev[len(b)]
}

// FuzzyFindTask returns the task whose name best matches the given query.
// Substring match is tried first; Levenshtein distance is used as a fallback.
func FuzzyFindTask(tasks []TaskSummary, name string) (TaskSummary, bool) {
	if len(tasks) == 0 {
		return TaskSummary{}, false
	}

	lower := strings.ToLower(name)

	for _, t := range tasks {
		if strings.Contains(strings.ToLower(t.Name), lower) {
			return t, true
		}
	}

	// Levenshtein fallback.
	best := tasks[0]
	bestDist := Levenshtein(tasks[0].Name, name)

	for _, t := range tasks[1:] {
		if d := Levenshtein(t.Name, name); d < bestDist {
			bestDist = d
			best = t
		}
	}

	return best, true
}

// ParseTaskStepID splits a stepID of the form "{index}-{name}" into its parts.
func ParseTaskStepID(stepID string) (int, string) {
	idx := strings.IndexByte(stepID, '-')
	if idx < 0 {
		return -1, stepID
	}

	n, err := strconv.Atoi(stepID[:idx])
	if err != nil {
		return -1, stepID
	}

	return n, stepID[idx+1:]
}

// ParseTaskSummaryPath supports both legacy task paths and backwards job paths.
func ParseTaskSummaryPath(p string) (int, string, bool) {
	trimmed := strings.TrimSpace(strings.Trim(p, "/"))
	if trimmed == "" {
		return 0, "", false
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) < 4 || parts[0] != "pipeline" {
		return 0, "", false
	}

	if parts[2] == "tasks" {
		idx, name := ParseTaskStepID(parts[3])

		return idx, name, true
	}

	if parts[2] != "jobs" || len(parts) < 7 {
		return 0, "", false
	}

	kindIndex := -1
	for i, part := range parts {
		if part == "tasks" || part == "agent" {
			kindIndex = i

			break
		}
	}

	if kindIndex < 0 || kindIndex+1 >= len(parts) {
		return 0, "", false
	}

	name := parts[kindIndex+1]
	if name == "" {
		return 0, "", false
	}

	for _, part := range parts[4:kindIndex] {
		idx, convErr := strconv.Atoi(part)
		if convErr == nil {
			return idx, name, true
		}
	}

	return 0, "", false
}

// TaskSummaryToMap converts a TaskSummary to a map for use as a tool result.
func TaskSummaryToMap(t TaskSummary) map[string]any {
	m := map[string]any{
		"name":   t.Name,
		"index":  t.Index,
		"status": t.Status,
	}

	if t.StartedAt != "" {
		m["started_at"] = t.StartedAt
	}

	if t.Elapsed != "" {
		m["elapsed"] = t.Elapsed
	}

	return m
}
