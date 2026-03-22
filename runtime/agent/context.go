package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/jtarchie/pocketci/runtime/agent/internal/helpers"
	pipelinerunner "github.com/jtarchie/pocketci/runtime/runner"
)

// AgentContextTask specifies a prior task whose output is pre-fetched into the
// agent's session history before the first turn.
type AgentContextTask struct {
	Name  string `yaml:"name"            json:"name"`
	Field string `yaml:"field,omitempty" json:"field,omitempty"` // "stdout" | "stderr" | "both" (default)
}

// AgentContextFile specifies a volume file whose contents are pre-read into the
// agent's session history before the first turn, saving a read tool call.
// Path is "mountname/relative/path" (e.g. "diff/pr.diff").
type AgentContextFile struct {
	Path     string `yaml:"path"                json:"path"`
	MaxBytes int    `yaml:"max_bytes,omitempty" json:"max_bytes,omitempty"`
}

// AgentContext configures pre-fetched task outputs and file contents injected
// as synthetic tool call events before the agent's first turn.
type AgentContext struct {
	Tasks    []AgentContextTask `yaml:"tasks,omitempty"     json:"tasks,omitempty"`
	Files    []AgentContextFile `yaml:"files,omitempty"     json:"files,omitempty"`
	MaxBytes int                `yaml:"max_bytes,omitempty" json:"max_bytes,omitempty"`
}

// injectSyntheticToolCall appends a matched FunctionCall + FunctionResponse
// event pair into the session history before the agent's first turn. This lets
// the agent read the result as if it had called the tool itself, saving a turn.
func injectSyntheticToolCall(
	ctx context.Context,
	svc session.Service,
	sess session.Session,
	agentName, toolName string,
	args map[string]any,
	result map[string]any,
) error {
	callID := uuid.NewString()
	invocationID := uuid.NewString()

	// Model "calls" the tool.
	callEvent := session.NewEvent(invocationID)
	callEvent.Author = agentName
	callEvent.LLMResponse = adkmodel.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						ID:   callID,
						Name: toolName,
						Args: args,
					},
				},
			},
		},
	}

	if err := svc.AppendEvent(ctx, sess, callEvent); err != nil {
		return fmt.Errorf("append synthetic call event: %w", err)
	}

	// Tool returns the result.
	respEvent := session.NewEvent(invocationID)
	respEvent.Author = agentName
	respEvent.LLMResponse = adkmodel.LLMResponse{
		Content: &genai.Content{
			Role: "user",
			Parts: []*genai.Part{
				{
					FunctionResponse: &genai.FunctionResponse{
						ID:       callID,
						Name:     toolName,
						Response: result,
					},
				},
			},
		},
	}
	respEvent.Actions.SkipSummarization = true

	if err := svc.AppendEvent(ctx, sess, respEvent); err != nil {
		return fmt.Errorf("append synthetic response event: %w", err)
	}

	return nil
}

// injectListTasksContext pre-injects a synthetic list_tasks result into the
// session so the agent knows the run state from turn 0.
func injectListTasksContext(
	ctx context.Context,
	svc session.Service,
	sess session.Session,
	config AgentConfig,
	now time.Time,
	auditEvents *[]AuditEvent,
) {
	if config.Storage == nil || config.RunID == "" {
		return
	}

	summaries, err := loadTaskSummaries(ctx, config.Storage, config.RunID)
	if err != nil || len(summaries) == 0 {
		return
	}

	taskMaps := make([]any, len(summaries))
	for i, t := range summaries {
		taskMaps[i] = helpers.TaskSummaryToMap(t)
	}

	listTasksResult := map[string]any{"tasks": taskMaps}

	_ = injectSyntheticToolCall(
		ctx, svc, sess,
		config.Name, "list_tasks",
		map[string]any{},
		listTasksResult,
	)

	AppendAuditEvent(auditEvents, AuditEvent{
		Timestamp:  now.Format(time.RFC3339),
		Author:     config.Name,
		Type:       "pre_context",
		ToolName:   "list_tasks",
		ToolArgs:   map[string]any{},
		ToolResult: listTasksResult,
	}, config.OnAuditEvent)
}

// injectTaskContexts pre-injects declared context tasks as synthetic
// get_task_result results into the session.
func injectTaskContexts(
	ctx context.Context,
	svc session.Service,
	sess session.Session,
	config AgentConfig,
	now time.Time,
	auditEvents *[]AuditEvent,
) {
	if config.Context == nil || config.Storage == nil || config.RunID == "" {
		return
	}

	maxBytes := config.Context.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 4096
	}

	summaries, _ := loadTaskSummaries(ctx, config.Storage, config.RunID)

	for _, ct := range config.Context.Tasks {
		matched, ok := helpers.FuzzyFindTask(summaries, ct.Name)
		if !ok {
			continue
		}

		taskKey := matched.Key
		if taskKey == "" {
			stepID := fmt.Sprintf("%d-%s", matched.Index, matched.Name)
			taskKey = "/pipeline/" + config.RunID + "/tasks/" + stepID
		}

		payload, err := config.Storage.Get(ctx, taskKey)
		if err != nil {
			continue
		}

		stdout, _ := payload["stdout"].(string)
		stderr, _ := payload["stderr"].(string)

		field := ct.Field
		if field == "" {
			field = "both"
		}

		switch field {
		case "stdout":
			stderr = ""
		case "stderr":
			stdout = ""
		}

		stdout, _ = helpers.TruncateStr(stdout, maxBytes)
		stderr, _ = helpers.TruncateStr(stderr, maxBytes)

		result := map[string]any{
			"name":  matched.Name,
			"index": matched.Index,
		}

		if s, ok := payload["status"].(string); ok {
			result["status"] = s
		}

		if v, ok := payload["code"].(float64); ok {
			result["exit_code"] = int(v)
		}

		if stdout != "" {
			result["stdout"] = stdout
		}

		if stderr != "" {
			result["stderr"] = stderr
		}

		getTaskArgs := map[string]any{"name": ct.Name}

		_ = injectSyntheticToolCall(
			ctx, svc, sess,
			config.Name, "get_task_result",
			getTaskArgs,
			result,
		)

		AppendAuditEvent(auditEvents, AuditEvent{
			Timestamp:  now.Format(time.RFC3339),
			Author:     config.Name,
			Type:       "pre_context",
			ToolName:   "get_task_result",
			ToolArgs:   getTaskArgs,
			ToolResult: result,
		}, config.OnAuditEvent)
	}
}

// injectFileContexts pre-injects declared context files as synthetic read_file
// results into the session using the sandbox to read the file contents.
func injectFileContexts(
	ctx context.Context,
	sandbox *pipelinerunner.SandboxHandle,
	svc session.Service,
	sess session.Session,
	config AgentConfig,
	now time.Time,
	auditEvents *[]AuditEvent,
) {
	if config.Context == nil || len(config.Context.Files) == 0 {
		return
	}

	for _, cf := range config.Context.Files {
		var execInput pipelinerunner.ExecInput
		execInput.Command.Path = "/bin/sh"
		execInput.Command.Args = []string{"-c", "cat " + cf.Path}

		execResult, execErr := sandbox.Exec(execInput)
		if execErr != nil || execResult.Code != 0 {
			continue // file not yet written or path wrong — skip silently
		}

		maxBytes := cf.MaxBytes
		if maxBytes <= 0 {
			maxBytes = config.Context.MaxBytes
		}

		if maxBytes <= 0 {
			maxBytes = 4096
		}

		content, truncated := helpers.TruncateStr(execResult.Stdout, maxBytes)

		fileResult := map[string]any{
			"path":    cf.Path,
			"content": content,
		}

		if truncated {
			fileResult["truncated"] = true
		}

		readFileArgs := map[string]any{"path": cf.Path}

		_ = injectSyntheticToolCall(
			ctx, svc, sess,
			config.Name, "read_file",
			readFileArgs,
			fileResult,
		)

		AppendAuditEvent(auditEvents, AuditEvent{
			Timestamp:  now.Format(time.RFC3339),
			Author:     config.Name,
			Type:       "pre_context",
			ToolName:   "read_file",
			ToolArgs:   readFileArgs,
			ToolResult: fileResult,
		}, config.OnAuditEvent)
	}
}
