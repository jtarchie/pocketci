package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/jtarchie/pocketci/runtime/agent/internal/helpers"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
)

const (
	DefaultToolTimeout      = "60s"
	DefaultRunScriptTimeout = "300s"

	writeFilePreviousContentMaxBytes = 4096
)

// execScript runs a shell script in the sandbox and returns the result.
// It handles the common exec+timeout pattern shared by all sandbox-backed tools.
func execScript(
	ctx adktool.Context,
	sandbox *pipelinerunner.SandboxHandle,
	script string,
	onOutput pipelinerunner.OutputCallback,
	timeout string,
	toolName string,
) (*pipelinerunner.RunResult, error) {
	var execInput pipelinerunner.ExecInput
	execInput.Command.Path = "/bin/sh"
	execInput.Command.Args = []string{"-c", script}
	execInput.OnOutput = onOutput
	execInput.Timeout = timeout

	result, err := sandbox.Exec(ctx, execInput)
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}

	if result.Status == pipelinerunner.RunAbort {
		return nil, fmt.Errorf("%s timed out after %s", toolName, timeout)
	}

	return result, nil
}

// execScriptWithEnv runs a shell script with additional environment variables.
func execScriptWithEnv(
	ctx adktool.Context,
	sandbox *pipelinerunner.SandboxHandle,
	script string,
	onOutput pipelinerunner.OutputCallback,
	timeout string,
	toolName string,
	env map[string]string,
) (*pipelinerunner.RunResult, error) {
	var execInput pipelinerunner.ExecInput
	execInput.Command.Path = "/bin/sh"
	execInput.Command.Args = []string{"-c", script}
	execInput.OnOutput = onOutput
	execInput.Timeout = timeout
	execInput.Env = env

	result, err := sandbox.Exec(ctx, execInput)
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}

	if result.Status == pipelinerunner.RunAbort {
		return nil, fmt.Errorf("%s timed out after %s", toolName, timeout)
	}

	return result, nil
}

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
	Path   string `json:"path"`             // "mountname/relative/path"
	Offset int    `json:"offset,omitempty"` // 1-based start line (default: 1)
	Limit  int    `json:"limit,omitempty"`  // number of lines to read (default: 2000)
}

// readFileOutput is the tool result schema for read_file.
type readFileOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
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
func newRunScriptTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback, timeout string) (adktool.Tool, error) {
	return functiontool.New[runScriptInput, runCommandOutput](
		functiontool.Config{
			Name:        "run_script",
			Description: "Run a multi-line shell script via /bin/sh. Add 'set -e' at the top to abort on the first failure. Volume paths are accessible as relative paths from the working directory.",
		},
		func(ctx adktool.Context, input runScriptInput) (runCommandOutput, error) {
			result, err := execScript(ctx, sandbox, input.Script, onOutput, timeout, "run_script")
			if err != nil {
				return runCommandOutput{}, err
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
func newReadFileTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback, timeout string) (adktool.Tool, error) {
	return functiontool.New[readFileInput, readFileOutput](
		functiontool.Config{
			Name:        "read_file",
			Description: "Read file contents with optional offset and limit. Output includes line numbers for easy reference. Path format: \"mountname/relative/path\" (e.g. \"diff/pr.diff\").",
		},
		func(ctx adktool.Context, input readFileInput) (readFileOutput, error) {
			offset := input.Offset
			if offset <= 0 {
				offset = 1
			}

			limit := input.Limit
			if limit <= 0 {
				limit = 2000
			}

			end := offset + limit - 1
			script := fmt.Sprintf("awk 'NR>=%d && NR<=%d {printf \"%%6d\\t%%s\\n\", NR, $0}' %s",
				offset, end, input.Path)

			result, err := execScript(ctx, sandbox, script, onOutput, timeout, "read_file")
			if err != nil {
				return readFileOutput{}, err
			}

			if result.Code != 0 {
				return readFileOutput{}, fmt.Errorf("read_file: %s exited %d: %s", input.Path, result.Code, result.Stderr)
			}

			return readFileOutput{
				Path:    input.Path,
				Content: result.Stdout,
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

// grepInput is the tool schema for grep.
type grepInput struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	GlobFilter      string `json:"glob_filter,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
}

// grepOutput is the tool result schema for grep.
type grepOutput struct {
	Matches string `json:"matches"`
	Count   int    `json:"count"`
}

// newGrepTool creates the grep tool backed by a sandbox.
func newGrepTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback, timeout string) (adktool.Tool, error) {
	return functiontool.New[grepInput, grepOutput](
		functiontool.Config{
			Name:        "grep",
			Description: "Search file contents with regex patterns. Returns matching lines in file:line:content format.",
		},
		func(ctx adktool.Context, input grepInput) (grepOutput, error) {
			path := input.Path
			if path == "" {
				path = "."
			}

			maxResults := input.MaxResults
			if maxResults <= 0 {
				maxResults = 100
			}

			var args []string
			args = append(args, "-rn")

			if input.CaseInsensitive {
				args = append(args, "-i")
			}

			if input.GlobFilter != "" {
				args = append(args, "--include="+input.GlobFilter)
			}

			args = append(args, "--", input.Pattern, path)

			script := fmt.Sprintf("grep %s | head -n %d", ShellJoinArgs(args), maxResults)

			result, err := execScript(ctx, sandbox, script, onOutput, timeout, "grep")
			if err != nil {
				return grepOutput{}, err
			}

			// Exit code 1 means no matches — not an error.
			if result.Code != 0 && result.Code != 1 {
				return grepOutput{}, fmt.Errorf("grep exited %d: %s", result.Code, result.Stderr)
			}

			matches := strings.TrimRight(result.Stdout, "\n")
			count := 0
			if matches != "" {
				count = strings.Count(matches, "\n") + 1
			}

			return grepOutput{
				Matches: matches,
				Count:   count,
			}, nil
		},
	)
}

// globInput is the tool schema for glob.
type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// globOutput is the tool result schema for glob.
type globOutput struct {
	Files []string `json:"files"`
	Count int      `json:"count"`
}

// newGlobTool creates the glob tool backed by a sandbox.
func newGlobTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback, timeout string) (adktool.Tool, error) {
	return functiontool.New[globInput, globOutput](
		functiontool.Config{
			Name:        "glob",
			Description: "Find files by name pattern (e.g. \"**/*.go\", \"*.yml\"). Returns matching file paths sorted alphabetically.",
		},
		func(ctx adktool.Context, input globInput) (globOutput, error) {
			path := input.Path
			if path == "" {
				path = "."
			}

			script := BuildFindCommand(input.Pattern, path)

			result, err := execScript(ctx, sandbox, script, onOutput, timeout, "glob")
			if err != nil {
				return globOutput{}, err
			}

			if result.Code != 0 {
				return globOutput{}, fmt.Errorf("glob exited %d: %s", result.Code, result.Stderr)
			}

			output := strings.TrimRight(result.Stdout, "\n")
			if output == "" {
				return globOutput{Files: []string{}, Count: 0}, nil
			}

			files := strings.Split(output, "\n")

			return globOutput{
				Files: files,
				Count: len(files),
			}, nil
		},
	)
}

// BuildFindCommand translates a glob pattern into a find command.
func BuildFindCommand(pattern, path string) string {
	const maxResults = 1000

	// "**/*.ext" → find path -name '*.ext' -type f
	if strings.HasPrefix(pattern, "**/") {
		namePattern := strings.TrimPrefix(pattern, "**/")
		return fmt.Sprintf("find %s -name '%s' -type f | sort | head -n %d", path, namePattern, maxResults)
	}

	// "dir/**/*.ext" → find path/dir -name '*.ext' -type f
	if idx := strings.Index(pattern, "/**/"); idx >= 0 {
		dir := pattern[:idx]
		namePattern := pattern[idx+4:]

		return fmt.Sprintf("find %s/%s -name '%s' -type f | sort | head -n %d", path, dir, namePattern, maxResults)
	}

	// "*.ext" (no **) → find path -maxdepth 1 -name '*.ext' -type f
	if !strings.Contains(pattern, "/") && strings.Contains(pattern, "*") {
		return fmt.Sprintf("find %s -maxdepth 1 -name '%s' -type f | sort | head -n %d", path, pattern, maxResults)
	}

	// Fallback: treat as a -path pattern
	return fmt.Sprintf("find %s -path '*%s*' -type f | sort | head -n %d", path, pattern, maxResults)
}

// writeFileInput is the tool schema for write_file.
type writeFileInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// writeFileOutput is the tool result schema for write_file.
type writeFileOutput struct {
	Path            string `json:"path"`
	Written         bool   `json:"written"`
	PreviousContent string `json:"previous_content,omitempty"`
	IsNew           bool   `json:"is_new"`
}

// newWriteFileTool creates the write_file tool backed by a sandbox.
func newWriteFileTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback, timeout string) (adktool.Tool, error) {
	return functiontool.New[writeFileInput, writeFileOutput](
		functiontool.Config{
			Name:        "write_file",
			Description: "Create or overwrite a file. Returns the previous file content (if any) for context. Content is delivered safely via base64 encoding. Parent directories are created automatically.",
		},
		func(ctx adktool.Context, input writeFileInput) (writeFileOutput, error) {
			// Read existing content before writing.
			var previousContent string
			isNew := true

			readScript := fmt.Sprintf("cat '%s' 2>/dev/null", input.FilePath)

			readResult, readErr := execScript(ctx, sandbox, readScript, nil, timeout, "write_file_read")
			if readErr == nil && readResult.Code == 0 && readResult.Stdout != "" {
				previousContent, _ = helpers.TruncateStr(readResult.Stdout, writeFilePreviousContentMaxBytes)
				isNew = false
			}

			// Write the file.
			encoded := base64.StdEncoding.EncodeToString([]byte(input.Content))
			script := fmt.Sprintf("mkdir -p \"$(dirname '%s')\" && printf '%%s' '%s' | base64 -d > '%s'",
				input.FilePath, encoded, input.FilePath)

			result, err := execScript(ctx, sandbox, script, onOutput, timeout, "write_file")
			if err != nil {
				return writeFileOutput{}, err
			}

			if result.Code != 0 {
				return writeFileOutput{}, fmt.Errorf("write_file: exited %d: %s", result.Code, result.Stderr)
			}

			return writeFileOutput{
				Path:            input.FilePath,
				Written:         true,
				PreviousContent: previousContent,
				IsNew:           isNew,
			}, nil
		},
	)
}

// EffectiveToolTimeouts returns the resolved timeouts for regular tools and
// run_script. If the config timeout is empty, defaults are used.
func EffectiveToolTimeouts(configTimeout string) (toolTimeout, scriptTimeout string) {
	toolTimeout = configTimeout
	if toolTimeout == "" {
		toolTimeout = DefaultToolTimeout
	}

	scriptTimeout = configTimeout
	if scriptTimeout == "" {
		scriptTimeout = DefaultRunScriptTimeout
	}

	return toolTimeout, scriptTimeout
}

// taskToolInput is the tool input schema for task tools.
// The LLM passes an optional request string that is set as TOOL_REQUEST env var.
type taskToolInput struct {
	Request string `json:"request"`
}

// taskToolOutput is the tool result schema for task tools.
type taskToolOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// newTaskTool creates a functiontool that runs a container command inside
// the agent's sandbox. The command, args, and env come from the ToolDef.
func newTaskTool(
	sandbox *pipelinerunner.SandboxHandle,
	toolDef ToolDef,
	parentConfig AgentConfig,
	timeout string,
) (adktool.Tool, error) {
	desc := toolDef.Description
	if desc == "" {
		desc = "Run task: " + toolDef.Name
	}

	return functiontool.New[taskToolInput, taskToolOutput](
		functiontool.Config{
			Name:        toolDef.Name,
			Description: desc,
		},
		func(ctx adktool.Context, input taskToolInput) (taskToolOutput, error) {
			// Build the shell command from config.run fields.
			script := toolDef.CommandPath
			if len(toolDef.CommandArgs) > 0 {
				script += " " + ShellJoinArgs(toolDef.CommandArgs)
			}

			// Merge tool env + optional request from the LLM.
			env := make(map[string]string, len(toolDef.Env)+1)
			for k, v := range toolDef.Env {
				env[k] = v
			}

			if input.Request != "" {
				env["TOOL_REQUEST"] = input.Request
			}

			result, err := execScriptWithEnv(ctx, sandbox, script, parentConfig.OnOutput, timeout, toolDef.Name, env)
			if err != nil {
				return taskToolOutput{}, err
			}

			return taskToolOutput{
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
				ExitCode: result.Code,
			}, nil
		},
	)
}

// ShellJoinArgs joins shell arguments with proper quoting.
func ShellJoinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg == "--" || strings.HasPrefix(arg, "-") {
			quoted[i] = arg
		} else {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		}
	}

	return strings.Join(quoted, " ")
}
