package agent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
)

// runCommandInput is the tool schema for run_command.
type runCommandInput struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// runCommandOutput is the tool result schema for run_command and run_script.
type runCommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// runScriptInput is the tool schema for run_script.
type runScriptInput struct {
	Script string `json:"script"`
}

// readFileInput is the tool schema for read_file.
type readFileInput struct {
	Path     string `json:"path"`                // "mountname/relative/path"
	MaxBytes int    `json:"max_bytes,omitempty"` // default 4096
}

// readFileOutput is the tool result schema for read_file.
type readFileOutput struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

// taskSummary is the list_tasks tool output element.
type taskSummary struct {
	Name      string `json:"name"`
	Index     int    `json:"index"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`
	Key       string `json:"-"`
}

// listTasksOutput is the list_tasks tool result.
type listTasksOutput struct {
	Tasks []taskSummary `json:"tasks"`
}

// getTaskResultInput is the get_task_result tool input schema.
type getTaskResultInput struct {
	Name     string `json:"name"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

// getTaskResultOutput is the get_task_result tool result schema.
type getTaskResultOutput struct {
	Name      string `json:"name"`
	Index     int    `json:"index"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	StartedAt string `json:"started_at,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`
	Truncated bool   `json:"truncated"`
}

// parseTaskStepID splits a stepID of the form "{index}-{name}" into its parts.
func parseTaskStepID(stepID string) (int, string) {
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

// loadTaskSummaries fetches all task summaries for the given run from storage.
func loadTaskSummaries(ctx context.Context, st storage.Driver, runID string) ([]taskSummary, error) {
	fields := []string{"status", "started_at", "elapsed"}

	legacyResults, err := st.GetAll(ctx, "/pipeline/"+runID+"/tasks/", fields)
	if err != nil {
		return nil, fmt.Errorf("load legacy tasks: %w", err)
	}

	jobResults, err := st.GetAll(ctx, "/pipeline/"+runID+"/jobs/", fields)
	if err != nil {
		return nil, fmt.Errorf("load job tasks: %w", err)
	}

	results := make(storage.Results, 0, len(legacyResults)+len(jobResults))
	results = append(results, legacyResults...)
	results = append(results, jobResults...)

	type taskKey struct {
		Index int
		Name  string
	}

	bestByKey := map[taskKey]taskSummary{}

	for _, r := range results {
		idx, name, ok := parseTaskSummaryPath(r.Path)
		if !ok {
			continue
		}

		t := taskSummary{Name: name, Index: idx, Key: r.Path}

		if s, ok := r.Payload["status"].(string); ok {
			t.Status = s
		}

		if s, ok := r.Payload["started_at"].(string); ok {
			t.StartedAt = s
		}

		if s, ok := r.Payload["elapsed"].(string); ok {
			t.Elapsed = s
		}

		key := taskKey{idx, name}

		if existing, exists := bestByKey[key]; !exists || (t.StartedAt != "" && existing.StartedAt == "") {
			bestByKey[key] = t
		}
	}

	tasks := make([]taskSummary, 0, len(bestByKey))
	for _, t := range bestByKey {
		tasks = append(tasks, t)
	}

	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Index != tasks[j].Index {
			return tasks[i].Index < tasks[j].Index
		}

		return tasks[i].Name < tasks[j].Name
	})

	return tasks, nil
}

// parseTaskSummaryPath supports both legacy task paths and backwards job paths.
func parseTaskSummaryPath(p string) (int, string, bool) {
	trimmed := strings.TrimSpace(strings.Trim(p, "/"))
	if trimmed == "" {
		return 0, "", false
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) < 4 || parts[0] != "pipeline" {
		return 0, "", false
	}

	if parts[2] == "tasks" {
		idx, name := parseTaskStepID(parts[3])

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

// levenshtein computes the edit distance between two strings (case-insensitive).
func levenshtein(a, b string) int {
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

// fuzzyFindTask returns the task whose name best matches the given query.
// Substring match is tried first; Levenshtein distance is used as a fallback.
func fuzzyFindTask(tasks []taskSummary, name string) (taskSummary, bool) {
	if len(tasks) == 0 {
		return taskSummary{}, false
	}

	lower := strings.ToLower(name)

	for _, t := range tasks {
		if strings.Contains(strings.ToLower(t.Name), lower) {
			return t, true
		}
	}

	// Levenshtein fallback.
	best := tasks[0]
	bestDist := levenshtein(tasks[0].Name, name)

	for _, t := range tasks[1:] {
		if d := levenshtein(t.Name, name); d < bestDist {
			bestDist = d
			best = t
		}
	}

	return best, true
}

// truncateStr shortens s to at most maxBytes bytes. Returns the (possibly
// truncated) string and a flag indicating whether truncation occurred.
func truncateStr(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}

	return s[:maxBytes], true
}

// taskSummaryToMap converts a taskSummary to a map for use as a tool result.
func taskSummaryToMap(t taskSummary) map[string]any {
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

// newRunCommandTool creates the run_command tool backed by a sandbox.
func newRunCommandTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback) (adktool.Tool, error) {
	return functiontool.New[runCommandInput, runCommandOutput](
		functiontool.Config{
			Name:        "run_command",
			Description: "Run a single executable with explicit args. Prefer run_script when you need multiple sequential shell steps.",
		},
		func(_ adktool.Context, input runCommandInput) (runCommandOutput, error) {
			var execInput pipelinerunner.ExecInput
			execInput.Command.Path = input.Command
			execInput.Command.Args = input.Args
			execInput.OnOutput = onOutput

			result, execErr := sandbox.Exec(execInput)
			if execErr != nil {
				return runCommandOutput{}, execErr
			}

			return runCommandOutput{
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
				ExitCode: result.Code,
			}, nil
		},
	)
}

// newRunScriptTool creates the run_script tool backed by a sandbox.
func newRunScriptTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback) (adktool.Tool, error) {
	return functiontool.New[runScriptInput, runCommandOutput](
		functiontool.Config{
			Name:        "run_script",
			Description: "Run a multi-line shell script via /bin/sh. Use this instead of run_command when executing multiple sequential steps — it avoids extra LLM round-trips. Add 'set -e' at the top to abort on the first failure. Volume paths are accessible as relative paths from the working directory.",
		},
		func(_ adktool.Context, input runScriptInput) (runCommandOutput, error) {
			var execInput pipelinerunner.ExecInput
			execInput.Command.Path = "/bin/sh"
			execInput.Command.Args = []string{"-c", input.Script}
			execInput.OnOutput = onOutput

			result, execErr := sandbox.Exec(execInput)
			if execErr != nil {
				return runCommandOutput{}, execErr
			}

			return runCommandOutput{
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
				ExitCode: result.Code,
			}, nil
		},
	)
}

// newReadFileTool creates the read_file tool backed by a sandbox.
func newReadFileTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback) (adktool.Tool, error) {
	return functiontool.New[readFileInput, readFileOutput](
		functiontool.Config{
			Name:        "read_file",
			Description: "Read the contents of a file from a mounted volume. Path format: \"mountname/relative/path\" (e.g. \"diff/pr.diff\"). Prefer this over run_script 'cat' when you only need to read a single file — it avoids a shell subprocess.",
		},
		func(_ adktool.Context, input readFileInput) (readFileOutput, error) {
			var execInput pipelinerunner.ExecInput
			execInput.Command.Path = "/bin/sh"
			execInput.Command.Args = []string{"-c", "cat " + input.Path}
			execInput.OnOutput = onOutput

			result, execErr := sandbox.Exec(execInput)
			if execErr != nil {
				return readFileOutput{}, execErr
			}

			if result.Code != 0 {
				return readFileOutput{}, fmt.Errorf("read_file: cat %s exited %d: %s", input.Path, result.Code, result.Stderr)
			}

			maxBytes := input.MaxBytes
			if maxBytes <= 0 {
				maxBytes = 4096
			}

			content, truncated := truncateStr(result.Stdout, maxBytes)

			return readFileOutput{
				Path:      input.Path,
				Content:   content,
				Truncated: truncated,
			}, nil
		},
	)
}

// newListTasksTool creates the list_tasks tool that queries storage.
func newListTasksTool(ctx context.Context, config AgentConfig) (adktool.Tool, error) {
	return functiontool.New[struct{}, listTasksOutput](
		functiontool.Config{
			Name:        "list_tasks",
			Description: "List all tasks executed in the current pipeline run with their name, status, start time, and elapsed duration.",
		},
		func(_ adktool.Context, _ struct{}) (listTasksOutput, error) {
			if config.Storage == nil || config.RunID == "" {
				return listTasksOutput{}, nil
			}

			tasks, err := loadTaskSummaries(ctx, config.Storage, config.RunID)
			if err != nil {
				return listTasksOutput{}, err
			}

			return listTasksOutput{Tasks: tasks}, nil
		},
	)
}

// newGetTaskResultTool creates the get_task_result tool that queries storage.
func newGetTaskResultTool(ctx context.Context, config AgentConfig) (adktool.Tool, error) {
	return functiontool.New[getTaskResultInput, getTaskResultOutput](
		functiontool.Config{
			Name:        "get_task_result",
			Description: "Retrieve the stdout, stderr, and exit code for a task in the current run. Use a partial or full task name; the closest match is returned.",
		},
		func(_ adktool.Context, input getTaskResultInput) (getTaskResultOutput, error) {
			if config.Storage == nil || config.RunID == "" {
				return getTaskResultOutput{}, fmt.Errorf("task storage not available")
			}

			summaries, err := loadTaskSummaries(ctx, config.Storage, config.RunID)
			if err != nil {
				return getTaskResultOutput{}, err
			}

			matched, ok := fuzzyFindTask(summaries, input.Name)
			if !ok {
				return getTaskResultOutput{}, fmt.Errorf("no tasks found in current run")
			}

			key := matched.Key
			if key == "" {
				stepID := fmt.Sprintf("%d-%s", matched.Index, matched.Name)
				key = "/pipeline/" + config.RunID + "/tasks/" + stepID
			}

			payload, err := config.Storage.Get(ctx, key)
			if err != nil {
				return getTaskResultOutput{}, fmt.Errorf("get task %q: %w", matched.Name, err)
			}

			maxBytes := input.MaxBytes
			if maxBytes <= 0 {
				maxBytes = 4096
			}

			out := getTaskResultOutput{
				Name:  matched.Name,
				Index: matched.Index,
			}

			if s, ok := payload["status"].(string); ok {
				out.Status = s
			}

			if v, ok := payload["code"].(float64); ok {
				out.ExitCode = int(v)
			}

			if s, ok := payload["started_at"].(string); ok {
				out.StartedAt = s
			}

			if s, ok := payload["elapsed"].(string); ok {
				out.Elapsed = s
			}

			stdout, _ := payload["stdout"].(string)
			stderr, _ := payload["stderr"].(string)

			var truncStdout, truncStderr bool

			out.Stdout, truncStdout = truncateStr(stdout, maxBytes)
			out.Stderr, truncStderr = truncateStr(stderr, maxBytes)
			out.Truncated = truncStdout || truncStderr

			return out, nil
		},
	)
}
