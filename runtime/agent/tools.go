package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/jtarchie/pocketci/runtime/agent/internal/helpers"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
)

// runCommandOutput is the tool result schema for run_script.
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

// TaskSummary is an alias for the helpers.TaskSummary type.
type TaskSummary = helpers.TaskSummary

// listTasksOutput is the list_tasks tool result.
type listTasksOutput struct {
	Tasks []TaskSummary `json:"tasks"`
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

// loadTaskSummaries fetches all task summaries for the given run from storage.
func loadTaskSummaries(ctx context.Context, st storage.Driver, runID string) ([]TaskSummary, error) {
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

	bestByKey := map[taskKey]TaskSummary{}

	for _, r := range results {
		idx, name, ok := helpers.ParseTaskSummaryPath(r.Path)
		if !ok {
			continue
		}

		t := TaskSummary{Name: name, Index: idx, Key: r.Path}

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

	tasks := make([]TaskSummary, 0, len(bestByKey))
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

// newRunScriptTool creates the run_script tool backed by a sandbox.
func newRunScriptTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback) (adktool.Tool, error) {
	return functiontool.New[runScriptInput, runCommandOutput](
		functiontool.Config{
			Name:        "run_script",
			Description: "Run a multi-line shell script via /bin/sh. Add 'set -e' at the top to abort on the first failure. Volume paths are accessible as relative paths from the working directory.",
		},
		func(ctx adktool.Context, input runScriptInput) (runCommandOutput, error) {
			var execInput pipelinerunner.ExecInput
			execInput.Command.Path = "/bin/sh"
			execInput.Command.Args = []string{"-c", input.Script}
			execInput.OnOutput = onOutput

			result, execErr := sandbox.Exec(ctx, execInput)
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
		func(ctx adktool.Context, input readFileInput) (readFileOutput, error) {
			var execInput pipelinerunner.ExecInput
			execInput.Command.Path = "/bin/sh"
			execInput.Command.Args = []string{"-c", "cat " + input.Path}
			execInput.OnOutput = onOutput

			result, execErr := sandbox.Exec(ctx, execInput)
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

			content, truncated := helpers.TruncateStr(result.Stdout, maxBytes)

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
					return getTaskResultOutput{}, errors.New("task storage not available")
			}

			summaries, err := loadTaskSummaries(ctx, config.Storage, config.RunID)
			if err != nil {
				return getTaskResultOutput{}, err
			}

			matched, ok := helpers.FuzzyFindTask(summaries, input.Name)
			if !ok {
					return getTaskResultOutput{}, errors.New("no tasks found in current run")
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

			out.Stdout, truncStdout = helpers.TruncateStr(stdout, maxBytes)
			out.Stderr, truncStderr = helpers.TruncateStr(stderr, maxBytes)
			out.Truncated = truncStdout || truncStderr

			return out, nil
		},
	)
}
