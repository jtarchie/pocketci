package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jtarchie/pocketci/storage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func buildMCPServer(store storage.Driver) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "ci-server",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: "Use these tools to inspect CI pipeline runs, tasks, and agents. " +
			"Start with get_run to get the run status, then list_run_tasks to see all tasks and their outputs. " +
			"Use get_run_task to fetch a single task with full payload fields (including long logs/audit/tool call data). " +
			"Use search_tasks with a run_id to search task logs within a specific run, " +
			"or with a pipeline_id to search across all runs for that pipeline (by ID, status, or error message).",
	})

	// Tool: get_run
	type GetRunInput struct {
		RunID string `json:"run_id" jsonschema:"The run ID to retrieve"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_run",
		Description: "Get the status and details of a pipeline run by its ID. Returns run status (queued/running/success/failed), timing, and any error message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input GetRunInput) (*mcp.CallToolResult, any, error) {
		run, err := store.GetRun(ctx, input.RunID)
		if err != nil {
			return nil, nil, fmt.Errorf("could not get run: %w", err)
		}

		data, err := json.Marshal(run)
		if err != nil {
			return nil, nil, fmt.Errorf("could not marshal run: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})

	// Tool: list_run_tasks
	type ListRunTasksInput struct {
		RunID string `json:"run_id" jsonschema:"The run ID whose tasks to list"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_run_tasks",
		Description: "List all tasks for a pipeline run. Returns each task's path, status, type (task/agent/pipeline), typed log output, elapsed time, and other details. Use this to identify which step failed and why.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ListRunTasksInput) (*mcp.CallToolResult, any, error) {
		fields := []string{"status", "elapsed", "started_at", "type", "text", "tokensUsed", "duration", "logs", "dependsOn"}
		prefix := fmt.Sprintf("/pipeline/%s/", input.RunID)

		results, err := store.GetAll(ctx, prefix, fields)
		if err != nil {
			return nil, nil, fmt.Errorf("could not get tasks: %w", err)
		}

		data, err := json.Marshal(results)
		if err != nil {
			return nil, nil, fmt.Errorf("could not marshal tasks: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})

	// Tool: get_run_task
	type GetRunTaskInput struct {
		RunID string `json:"run_id" jsonschema:"The run ID containing the task"`
		Path  string `json:"path"   jsonschema:"Task path, either absolute (/pipeline/<run>/...) or relative to the run prefix"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_run_task",
		Description: "Get a single task payload for a run. Returns full stored payload fields (for example: logs, usage, audit_log).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input GetRunTaskInput) (*mcp.CallToolResult, any, error) {
		if input.Path == "" {
			return nil, nil, fmt.Errorf("path is required")
		}

		prefix := fmt.Sprintf("/pipeline/%s/", input.RunID)
		lookupPath := input.Path
		if !strings.HasPrefix(lookupPath, "/") {
			lookupPath = prefix + strings.TrimPrefix(lookupPath, "/")
		}

		if !strings.HasPrefix(lookupPath, prefix) {
			return nil, nil, fmt.Errorf("task path must be scoped to the run")
		}

		payload, err := store.Get(ctx, lookupPath)
		if err != nil {
			return nil, nil, fmt.Errorf("could not get task: %w", err)
		}

		result := []storage.Result{{
			Path:    lookupPath,
			Payload: payload,
		}}

		data, err := json.Marshal(result)
		if err != nil {
			return nil, nil, fmt.Errorf("could not marshal task: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})

	// Tool: search_tasks
	type SearchTasksInput struct {
		RunID      string `json:"run_id,omitempty"      jsonschema:"The run ID to search task output within (provide either run_id or pipeline_id)"`
		PipelineID string `json:"pipeline_id,omitempty" jsonschema:"The pipeline ID to search runs within (provide either run_id or pipeline_id)"`
		Query      string `json:"query"                 jsonschema:"Full-text search query (FTS5 syntax)"`
		Page       *int   `json:"page,omitempty"        jsonschema:"Page number 1-based (default 1, only used with pipeline_id)"`
		PerPage    *int   `json:"per_page,omitempty"    jsonschema:"Results per page (default 20, only used with pipeline_id)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_tasks",
		Description: "Full-text search in two modes: " +
			"(1) provide run_id to search task logs within a specific run — useful for finding error messages or stack traces; " +
			"(2) provide pipeline_id to search across all runs for that pipeline by run ID, status, or error message — mirrors the pipeline runs search in the web UI.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SearchTasksInput) (*mcp.CallToolResult, any, error) {
		if input.RunID == "" && input.PipelineID == "" {
			return nil, nil, fmt.Errorf("either run_id or pipeline_id must be provided")
		}

		// Mode 1: search task output within a specific run
		if input.RunID != "" {
			prefix := fmt.Sprintf("/pipeline/%s/", input.RunID)

			results, err := store.Search(ctx, prefix, input.Query)
			if err != nil {
				return nil, nil, fmt.Errorf("could not search tasks: %w", err)
			}

			data, err := json.Marshal(results)
			if err != nil {
				return nil, nil, fmt.Errorf("could not marshal search results: %w", err)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
			}, nil, nil
		}

		// Mode 2: search runs for a pipeline (mirrors the web UI runs-search endpoint)
		page := 1
		if input.Page != nil && *input.Page > 0 {
			page = *input.Page
		}

		perPage := 20
		if input.PerPage != nil && *input.PerPage > 0 {
			perPage = *input.PerPage
		}

		results, err := store.SearchRunsByPipeline(ctx, input.PipelineID, input.Query, page, perPage)
		if err != nil {
			return nil, nil, fmt.Errorf("could not search runs: %w", err)
		}

		data, err := json.Marshal(results)
		if err != nil {
			return nil, nil, fmt.Errorf("could not marshal search results: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})

	// Tool: search_pipelines
	type SearchPipelinesInput struct {
		Query   string `json:"query"    jsonschema:"Search query matching pipeline name or content (empty returns all)"`
		Page    *int   `json:"page,omitempty"     jsonschema:"Page number 1-based (default 1)"`
		PerPage *int   `json:"per_page,omitempty" jsonschema:"Results per page (default 20)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_pipelines",
		Description: "Search pipelines by name or pipeline content using full-text search. Returns paginated results including pipeline IDs, names, and drivers.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SearchPipelinesInput) (*mcp.CallToolResult, any, error) {
		page := 1
		if input.Page != nil && *input.Page > 0 {
			page = *input.Page
		}

		perPage := 20
		if input.PerPage != nil && *input.PerPage > 0 {
			perPage = *input.PerPage
		}

		result, err := store.SearchPipelines(ctx, input.Query, page, perPage)
		if err != nil {
			return nil, nil, fmt.Errorf("could not search pipelines: %w", err)
		}

		data, err := json.Marshal(result)
		if err != nil {
			return nil, nil, fmt.Errorf("could not marshal pipelines: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})

	return s
}

func newMCPHandler(store storage.Driver) http.Handler {
	mcpServer := buildMCPServer(store)

	return mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return mcpServer
	}, nil)
}
