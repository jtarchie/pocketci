package agent

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/secrets"
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

// TaskSummary is the list_tasks tool output element.
type TaskSummary struct {
	Name      string `json:"name"`
	Index     int    `json:"index"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`
	Key       string `json:"-"`
}

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
		idx, name, ok := ParseTaskSummaryPath(r.Path)
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

// TruncateStr shortens s to at most maxBytes bytes. Returns the (possibly
// truncated) string and a flag indicating whether truncation occurred.
func TruncateStr(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}

	return s[:maxBytes], true
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

// newRunScriptTool creates the run_script tool backed by a sandbox.
func newRunScriptTool(sandbox *pipelinerunner.SandboxHandle, onOutput pipelinerunner.OutputCallback) (adktool.Tool, error) {
	return functiontool.New[runScriptInput, runCommandOutput](
		functiontool.Config{
			Name:        "run_script",
			Description: "Run a multi-line shell script via /bin/sh. Add 'set -e' at the top to abort on the first failure. Volume paths are accessible as relative paths from the working directory.",
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

			content, truncated := TruncateStr(result.Stdout, maxBytes)

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

// callAgentInput is the tool input schema for sub-agent tools (both modes).
// The LLM passes a plain-text request to the sub-agent.
type callAgentInput struct {
	Request string `json:"request"`
}

// callAgentOutput is the tool result schema for own-container sub-agent calls.
type callAgentOutput struct {
	Result string `json:"result"`
	Status string `json:"status"`
}

// buildSubAgentTool creates an ADK tool for the given sub-agent configuration.
//
// Shared-container mode (sub-agent image matches or is empty): wraps an ADK
// llmagent as an agenttool so the parent LLM can call it directly.
//
// Own-container mode (sub-agent declares a different image): registers a
// functiontool that spins up a separate sandbox, runs the sub-agent to
// completion, persists results to a nested storage path, and returns the
// final text to the parent.
func buildSubAgentTool(
	ctx context.Context,
	sandbox *pipelinerunner.SandboxHandle,
	sandboxRunner pipelinerunner.Runner,
	sm secrets.Manager,
	pipelineID string,
	subCfg SubAgentConfig,
	parentConfig AgentConfig,
) (adktool.Tool, error) {
	subImage := subCfg.Image
	if subImage == "" {
		subImage = parentConfig.Image
	}

	subModel := subCfg.Model
	if subModel == "" {
		subModel = parentConfig.Model
	}

	if subImage == parentConfig.Image {
		// Shared-container: build a functiontool that runs the sub-agent in
		// its own ADK session (reusing the parent's sandbox), collects the
		// full audit log + usage, persists to storage, and returns the result.
		provider, modelName := splitModel(subModel)

		apiKey := resolveSecret(ctx, sm, pipelineID, "agent/"+provider)
		if apiKey == "" {
			envKey := strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
			apiKey = os.Getenv(envKey)
		}

		subLLM, err := resolveModel(provider, modelName, apiKey, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("resolve model: %w", err)
		}

		// Sub-agent reuses the same sandbox tools as the parent.
		subRunScript, err := newRunScriptTool(sandbox, parentConfig.OnOutput)
		if err != nil {
			return nil, fmt.Errorf("run_script tool: %w", err)
		}

		subReadFile, err := newReadFileTool(sandbox, parentConfig.OnOutput)
		if err != nil {
			return nil, fmt.Errorf("read_file tool: %w", err)
		}

		subListTasks, err := newListTasksTool(ctx, parentConfig)
		if err != nil {
			return nil, fmt.Errorf("list_tasks tool: %w", err)
		}

		subGetTaskResult, err := newGetTaskResultTool(ctx, parentConfig)
		if err != nil {
			return nil, fmt.Errorf("get_task_result tool: %w", err)
		}

		subAgent, err := llmagent.New(llmagent.Config{
			Name:        subCfg.Name,
			Model:       subLLM,
			Description: fmt.Sprintf("Specialist sub-agent: %s. Call this when you need its expertise.", subCfg.Name),
			Instruction: subCfg.Prompt,
			Tools:       []adktool.Tool{subRunScript, subReadFile, subListTasks, subGetTaskResult},
		})
		if err != nil {
			return nil, fmt.Errorf("create sub-agent: %w", err)
		}

		return newSharedContainerSubAgentTool(ctx, subCfg, subAgent, parentConfig)
	}

	// Own-container: custom functiontool that spins up a separate sandbox.
	return newCallAgentTool(ctx, sandboxRunner, sm, pipelineID, subCfg, subModel, parentConfig)
}

// newSharedContainerSubAgentTool builds a functiontool that runs a sub-agent
// in its own ADK session while reusing the parent's sandbox container and tools.
// It collects the full audit log, usage, and final text, then persists them to
// {storageKeyPrefix}/sub-agents/{name}/run so the UI and MCP tools can access
// each sub-agent's results individually.
func newSharedContainerSubAgentTool(
	ctx context.Context,
	subCfg SubAgentConfig,
	subAgent agent.Agent,
	parentConfig AgentConfig,
) (adktool.Tool, error) {
	return functiontool.New[callAgentInput, callAgentOutput](
		functiontool.Config{
			Name:        subCfg.Name,
			Description: fmt.Sprintf("Specialist sub-agent: %s. Call this when you need its expertise.", subCfg.Name),
		},
		func(_ adktool.Context, input callAgentInput) (callAgentOutput, error) {
			prompt := subCfg.Prompt
			if input.Request != "" {
				if prompt != "" {
					prompt = prompt + "\n\nSpecific request: " + input.Request
				} else {
					prompt = input.Request
				}
			}

			// Create an in-memory session for the sub-agent.
			sessionService := session.InMemoryService()

			sessResp, err := sessionService.Create(ctx, &session.CreateRequest{
				AppName: "ci-sub-agent",
				UserID:  "pipeline",
			})
			if err != nil {
				return callAgentOutput{}, fmt.Errorf("create session: %w", err)
			}

			// Append the user message to the session.
			userInvID := uuid.NewString()
			userEvent := session.NewEvent(userInvID)
			userEvent.Author = "user"
			userEvent.LLMResponse = adkmodel.LLMResponse{
				Content: genai.NewContentFromText(prompt, genai.RoleUser),
			}

			_ = sessionService.AppendEvent(ctx, sessResp.Session, userEvent)

			runnr, err := runner.New(runner.Config{
				AppName:        "ci-sub-agent",
				Agent:          subAgent,
				SessionService: sessionService,
			})
			if err != nil {
				return callAgentOutput{}, fmt.Errorf("create runner: %w", err)
			}

			// Run the sub-agent, collecting text, audit events, and usage.
			var textBuilder strings.Builder
			var auditEvents []AuditEvent
			var usage AgentUsage
			now := time.Now().UTC()

			AppendAuditEvent(&auditEvents, AuditEvent{
				Timestamp: now.Format(time.RFC3339),
				Author:    "user",
				Type:      "user_message",
				Text:      prompt,
			}, nil)

			for event, err := range runnr.Run(ctx, "pipeline", sessResp.Session.ID(), nil, agent.RunConfig{}) {
				if err != nil {
					return callAgentOutput{}, fmt.Errorf("sub-agent run: %w", err)
				}

				if event.UsageMetadata != nil {
					accumulateUsage(&usage, event.UsageMetadata, nil)
				}

				if event.Content == nil {
					continue
				}

				ts := now.Format(time.RFC3339)
				if !event.Timestamp.IsZero() {
					ts = event.Timestamp.UTC().Format(time.RFC3339)
				}

				isFinal := event.IsFinalResponse()

				for _, part := range event.Content.Parts {
					if part.FunctionCall != nil {
						fc := part.FunctionCall
						usage.ToolCallCount++

						AppendAuditEvent(&auditEvents, AuditEvent{
							Timestamp:    ts,
							InvocationID: event.InvocationID,
							Author:       event.Author,
							Type:         "tool_call",
							ToolName:     fc.Name,
							ToolCallID:   fc.ID,
							ToolArgs:     fc.Args,
						}, nil)
					}

					if part.FunctionResponse != nil {
						fr := part.FunctionResponse

						AppendAuditEvent(&auditEvents, AuditEvent{
							Timestamp:    ts,
							InvocationID: event.InvocationID,
							Author:       event.Author,
							Type:         "tool_response",
							ToolName:     fr.Name,
							ToolCallID:   fr.ID,
							ToolResult:   fr.Response,
						}, nil)
					}

					if part.Text != "" {
						eventType := "model_text"
						if isFinal {
							eventType = "model_final"
						}

						AppendAuditEvent(&auditEvents, AuditEvent{
							Timestamp:    ts,
							InvocationID: event.InvocationID,
							Author:       event.Author,
							Type:         eventType,
							Text:         part.Text,
						}, nil)

						if isFinal {
							textBuilder.WriteString(part.Text)
						}
					}
				}
			}

			finalText := textBuilder.String()
			status := "success"

			if finalText == "" {
				status = "error"
			}

			// Persist to storage so the UI tree and MCP tools can show
			// each sub-agent as a separate nested entry.
			if subCfg.StorageKeyPrefix != "" && parentConfig.Storage != nil {
				storageKey := subCfg.StorageKeyPrefix + "/sub-agents/" + subCfg.Name + "/run"
				_ = parentConfig.Storage.Set(ctx, storageKey, map[string]any{
					"status":    status,
					"stdout":    finalText,
					"usage":     usage,
					"audit_log": auditEvents,
				})
			}

			return callAgentOutput{
				Result: finalText,
				Status: status,
			}, nil
		},
	)
}

// newCallAgentTool builds a functiontool that runs a sub-agent in its own
// sandbox container. Used when the sub-agent's image differs from the parent's.
// Results are persisted at {storageKeyPrefix}/sub-agents/{name}/run so the
// UI automatically shows them nested under the parent agent step.
func newCallAgentTool(
	ctx context.Context,
	sandboxRunner pipelinerunner.Runner,
	sm secrets.Manager,
	pipelineID string,
	subCfg SubAgentConfig,
	subModel string,
	parentConfig AgentConfig,
) (adktool.Tool, error) {
	return functiontool.New[callAgentInput, callAgentOutput](
		functiontool.Config{
			Name:        subCfg.Name,
			Description: fmt.Sprintf("Specialist sub-agent: %s. Call this when you need its expertise.", subCfg.Name),
		},
		func(_ adktool.Context, input callAgentInput) (callAgentOutput, error) {
			prompt := subCfg.Prompt
			if input.Request != "" {
				if prompt != "" {
					prompt = prompt + "\n\nSpecific request: " + input.Request
				} else {
					prompt = input.Request
				}
			}

			subAgentConfig := AgentConfig{
				Name:        subCfg.Name,
				Prompt:      prompt,
				Model:       subModel,
				Image:       subCfg.Image,
				Mounts:      parentConfig.Mounts,
				Storage:     parentConfig.Storage,
				RunID:       parentConfig.RunID,
				Namespace:   parentConfig.Namespace,
				PipelineID:  parentConfig.PipelineID,
				TriggeredBy: parentConfig.TriggeredBy,
				OnOutput:    parentConfig.OnOutput,
			}

			result, err := RunAgent(ctx, sandboxRunner, sm, pipelineID, subAgentConfig)
			if err != nil {
				return callAgentOutput{}, err
			}

			// Persist to a nested storage path so the UI tree renders the
			// sub-agent's result indented under the parent agent step.
			if subCfg.StorageKeyPrefix != "" && parentConfig.Storage != nil {
				storageKey := subCfg.StorageKeyPrefix + "/sub-agents/" + subCfg.Name + "/run"
				_ = parentConfig.Storage.Set(ctx, storageKey, map[string]any{
					"status":    result.Status,
					"stdout":    result.Text,
					"usage":     result.Usage,
					"audit_log": result.AuditLog,
				})
			}

			return callAgentOutput{
				Result: result.Text,
				Status: result.Status,
			}, nil
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

			matched, ok := FuzzyFindTask(summaries, input.Name)
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

			out.Stdout, truncStdout = TruncateStr(stdout, maxBytes)
			out.Stderr, truncStderr = TruncateStr(stderr, maxBytes)
			out.Truncated = truncStdout || truncStderr

			return out, nil
		},
	)
}
