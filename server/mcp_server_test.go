package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/gomega"
)

// newMCPTestSession creates a router with an HTTP test server, connects an MCP
// client via StreamableHTTP, and returns the session and the storage driver.
func newMCPTestSession(t *testing.T) (*mcp.ClientSession, storage.Driver) {
	t.Helper()
	assert := NewWithT(t)

	buildFile, err := os.CreateTemp(t.TempDir(), "")
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = buildFile.Close() })

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: buildFile.Name()}, "namespace", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = store.Close() })

	router, err := server.NewRouter(slog.Default(), store, server.RouterOptions{})
	assert.Expect(err).NotTo(HaveOccurred())

	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	session, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = session.Close() })

	return session, store
}

func TestMCPListTools(t *testing.T) {
	t.Parallel()

	session, _ := newMCPTestSession(t)

	result, err := session.ListTools(context.Background(), nil)
	assert := NewWithT(t)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result.Tools).To(HaveLen(5))

	names := make([]string, len(result.Tools))
	for i, tool := range result.Tools {
		names[i] = tool.Name
	}
	assert.Expect(names).To(ConsistOf("get_run", "list_run_tasks", "get_run_task", "search_tasks", "search_pipelines"))
}

func TestMCPGetRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, store := newMCPTestSession(t)

	pipeline, err := store.SavePipeline(ctx, "test-pipeline", "export const pipeline = async () => {};", "native", "")
	NewWithT(t).Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(ctx, pipeline.ID)
	NewWithT(t).Expect(err).NotTo(HaveOccurred())

	t.Run("returns run details for a valid run ID", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_run",
			Arguments: map[string]any{"run_id": run.ID},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeFalse())
		assert.Expect(result.Content).To(HaveLen(1))

		text := result.Content[0].(*mcp.TextContent).Text
		var gotRun storage.PipelineRun
		assert.Expect(json.Unmarshal([]byte(text), &gotRun)).NotTo(HaveOccurred())
		assert.Expect(gotRun.ID).To(Equal(run.ID))
		assert.Expect(gotRun.PipelineID).To(Equal(pipeline.ID))
		assert.Expect(string(gotRun.Status)).To(Equal("queued"))
	})

	t.Run("returns error for a non-existent run ID", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "get_run",
			Arguments: map[string]any{"run_id": "no-such-run"},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeTrue())
	})
}

func TestMCPListRunTasks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, store := newMCPTestSession(t)
	assert := NewWithT(t)

	pipeline, err := store.SavePipeline(ctx, "tasks-pipeline", "export const pipeline = async () => {};", "native", "")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(ctx, pipeline.ID)
	assert.Expect(err).NotTo(HaveOccurred())

	err = store.Set(ctx, "/pipeline/"+run.ID+"/tasks/echo", map[string]any{
		"status": "success",
		"logs":   []map[string]any{{"type": "stdout", "content": "hello world"}},
		"type":   "task",
	})
	assert.Expect(err).NotTo(HaveOccurred())

	err = store.Set(ctx, "/pipeline/"+run.ID+"/tasks/build", map[string]any{
		"status": "failed",
		"logs":   []map[string]any{{"type": "stderr", "content": "build failed: exit 1"}},
		"type":   "task",
	})
	assert.Expect(err).NotTo(HaveOccurred())

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_run_tasks",
		Arguments: map[string]any{"run_id": run.ID},
	})
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(result.IsError).To(BeFalse())
	assert.Expect(result.Content).To(HaveLen(1))

	text := result.Content[0].(*mcp.TextContent).Text
	var tasks storage.Results
	assert.Expect(json.Unmarshal([]byte(text), &tasks)).NotTo(HaveOccurred())
	assert.Expect(tasks).To(HaveLen(2))
}

func TestMCPGetRunTask(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, store := newMCPTestSession(t)
	assert := NewWithT(t)

	pipeline, err := store.SavePipeline(ctx, "single-task-pipeline", "export const pipeline = async () => {};", "native", "")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(ctx, pipeline.ID)
	assert.Expect(err).NotTo(HaveOccurred())

	taskPath := "/pipeline/" + run.ID + "/jobs/review-pr/1/agent/code-quality-reviewer"
	err = store.Set(ctx, taskPath, map[string]any{
		"status":    "running",
		"logs":      []map[string]any{{"type": "stdout", "content": "line 1\nline 2\n"}},
		"audit_log": []any{map[string]any{"type": "tool_call", "toolName": "run_script"}},
	})
	assert.Expect(err).NotTo(HaveOccurred())

	t.Run("returns full payload for absolute path", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "get_run_task",
			Arguments: map[string]any{
				"run_id": run.ID,
				"path":   taskPath,
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeFalse())

		text := result.Content[0].(*mcp.TextContent).Text
		var got []storage.Result
		assert.Expect(json.Unmarshal([]byte(text), &got)).NotTo(HaveOccurred())
		assert.Expect(got).To(HaveLen(1))
		assert.Expect(got[0].Path).To(Equal(taskPath))
		assert.Expect(got[0].Payload["audit_log"]).NotTo(BeNil())
	})

	t.Run("accepts path relative to run prefix", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "get_run_task",
			Arguments: map[string]any{
				"run_id": run.ID,
				"path":   "jobs/review-pr/1/agent/code-quality-reviewer",
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeFalse())
	})

	t.Run("rejects task path outside run scope", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "get_run_task",
			Arguments: map[string]any{
				"run_id": run.ID,
				"path":   "/pipeline/other-run/jobs/review-pr/1/agent/code-quality-reviewer",
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeTrue())
	})
}

func TestMCPSearchTasks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, store := newMCPTestSession(t)
	assert := NewWithT(t)

	pipeline, err := store.SavePipeline(ctx, "search-pipeline", "export const pipeline = async () => {};", "native", "")
	assert.Expect(err).NotTo(HaveOccurred())

	run, err := store.SaveRun(ctx, pipeline.ID)
	assert.Expect(err).NotTo(HaveOccurred())

	err = store.Set(ctx, "/pipeline/"+run.ID+"/tasks/echo", map[string]any{
		"status": "success",
		"logs":   []map[string]any{{"type": "stdout", "content": "unique-token-xyz hello"}},
	})
	assert.Expect(err).NotTo(HaveOccurred())

	err = store.Set(ctx, "/pipeline/"+run.ID+"/tasks/other", map[string]any{
		"status": "success",
		"logs":   []map[string]any{{"type": "stdout", "content": "something else entirely"}},
	})
	assert.Expect(err).NotTo(HaveOccurred())

	t.Run("run_id mode searches task output within a run", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "search_tasks",
			Arguments: map[string]any{
				"run_id": run.ID,
				"query":  "unique-token-xyz",
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeFalse())

		text := result.Content[0].(*mcp.TextContent).Text
		var tasks storage.Results
		assert.Expect(json.Unmarshal([]byte(text), &tasks)).NotTo(HaveOccurred())
		assert.Expect(tasks).To(HaveLen(1))
	})

	t.Run("pipeline_id mode searches runs for a pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		// create a second run with a distinct error so it shows up in search
		run2, err := store.SaveRun(ctx, pipeline.ID)
		assert.Expect(err).NotTo(HaveOccurred())
		err = store.UpdateRunStatus(ctx, run2.ID, storage.RunStatusFailed, "unique-pipeline-error-token")
		assert.Expect(err).NotTo(HaveOccurred())

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "search_tasks",
			Arguments: map[string]any{
				"pipeline_id": pipeline.ID,
				"query":       "unique-pipeline-error-token",
			},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeFalse())

		text := result.Content[0].(*mcp.TextContent).Text
		var page storage.PaginationResult[storage.PipelineRun]
		assert.Expect(json.Unmarshal([]byte(text), &page)).NotTo(HaveOccurred())
		assert.Expect(page.Items).To(HaveLen(1))
		assert.Expect(page.Items[0].ID).To(Equal(run2.ID))
	})

	t.Run("returns error when neither run_id nor pipeline_id is provided", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "search_tasks",
			Arguments: map[string]any{"query": "anything"},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeTrue())
	})
}

func TestMCPSearchPipelines(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, store := newMCPTestSession(t)
	root := NewWithT(t)

	_, err := store.SavePipeline(ctx, "alpha-pipeline", "export const pipeline = async () => {};", "native", "")
	root.Expect(err).NotTo(HaveOccurred())
	_, err = store.SavePipeline(ctx, "beta-pipeline", "export const pipeline = async () => {};", "native", "")
	root.Expect(err).NotTo(HaveOccurred())

	t.Run("empty query returns all pipelines", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "search_pipelines",
			Arguments: map[string]any{"query": ""},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeFalse())

		text := result.Content[0].(*mcp.TextContent).Text
		var page storage.PaginationResult[storage.Pipeline]
		assert.Expect(json.Unmarshal([]byte(text), &page)).NotTo(HaveOccurred())
		assert.Expect(page.TotalItems).To(Equal(2))
	})

	t.Run("name query returns matching pipeline", func(t *testing.T) {
		t.Parallel()
		assert := NewWithT(t)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "search_pipelines",
			Arguments: map[string]any{"query": "alpha"},
		})
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(result.IsError).To(BeFalse())

		text := result.Content[0].(*mcp.TextContent).Text
		var page storage.PaginationResult[storage.Pipeline]
		assert.Expect(json.Unmarshal([]byte(text), &page)).NotTo(HaveOccurred())
		assert.Expect(page.TotalItems).To(Equal(1))
		assert.Expect(page.Items[0].Name).To(Equal("alpha-pipeline"))
	})
}
