package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// WebRunsController handles HTML view endpoints for pipeline runs.
type WebRunsController struct {
	BaseController
}

// TaskStats holds server-computed counts for task statuses.
type TaskStats struct {
	Success int
	Failure int
	Pending int
}

// countTaskStats walks the tree and counts task statuses.
func countTaskStats(tree *storage.Tree[storage.Payload]) TaskStats {
	var stats TaskStats
	countTaskStatsRecursive(tree, &stats)

	return stats
}

func countTaskStatsRecursive(node *storage.Tree[storage.Payload], stats *TaskStats) {
	if node == nil {
		return
	}

	if node.IsLeaf() && node.Name != "" {
		status, _ := node.Value["status"].(string)
		switch status {
		case "success":
			stats.Success++
		case "failure", "error":
			stats.Failure++
		default:
			stats.Pending++
		}
	}

	for _, child := range node.Children {
		countTaskStatsRecursive(child, stats)
	}
}

// preloadTerminalHTML fetches logs for all tasks and injects "terminalHTML"
// into each leaf node's Payload. This lets templates render terminal output
// inline without a separate lazy-loading request.
func (c *WebRunsController) preloadTerminalHTML(ctx *echo.Context, lookupPath string, tree *storage.Tree[storage.Payload]) {
	c.preloadTerminalHTMLWithOptions(ctx, lookupPath, tree, "/terminal", func(s string) string { return s })
}

// preloadTerminalHTMLWithOptions is the parameterized variant used by the share
// controller. terminalBaseURL is prepended to the task path in HTMX reload
// attributes (use "/terminal" for normal views). redact is applied to the
// rendered HTML before storage — pass an identity function for no redaction.
func (c *WebRunsController) preloadTerminalHTMLWithOptions(ctx *echo.Context, lookupPath string, tree *storage.Tree[storage.Payload], terminalBaseURL string, redact func(string) string) {
	stdoutResults, err := c.store.GetAll(ctx.Request().Context(), lookupPath, []string{"logs", "stdout", "stderr", "status", "error_message"})
	if err != nil {
		return
	}

	// Build a map from path to terminal HTML.
	htmlByPath := make(map[string]template.HTML, len(stdoutResults))
	for _, r := range stdoutResults {
		logs := ParseTerminalLogs(r.Payload["logs"])
		stdout, _ := r.Payload["stdout"].(string)
		stderr, _ := r.Payload["stderr"].(string)
		status, _ := r.Payload["status"].(string)
		errorMessage, _ := r.Payload["error_message"].(string)

		html := ToTerminalHTMLFromLogs(logs)
		if html == "" {
			displayOutput := stdout + stderr
			if displayOutput == "" && errorMessage != "" {
				displayOutput = errorMessage
			}

			html = ToTerminalHTML(displayOutput)
		}

		terminalID := SanitizeTerminalID(r.Path)
		html = WrapTerminalLines(html, terminalID)
		html = redact(html)

		if status == "running" || status == "" {
			htmlByPath[r.Path] = template.HTML(fmt.Sprintf(
				`<div class="term-container" hx-get="%s%s" hx-trigger="load delay:2s" hx-swap="outerHTML">%s</div>`,
				terminalBaseURL, r.Path, html,
			))
		} else {
			htmlByPath[r.Path] = template.HTML(fmt.Sprintf(
				`<div class="term-container">%s</div>`, html,
			))
		}
	}

	injectTerminalHTML(tree, htmlByPath)
}

// Terminal handles GET /terminal/* and returns a rendered task terminal fragment.
func (c *WebRunsController) Terminal(ctx *echo.Context) error {
	lookupPath := "/" + strings.TrimPrefix(ctx.Param("*"), "/")
	if lookupPath == "/" {
		return ctx.HTML(http.StatusNotFound, `<div class="term-container"></div>`)
	}

	payload, err := c.store.Get(ctx.Request().Context(), lookupPath)
	if err != nil {
		return ctx.HTML(http.StatusNotFound, `<div class="term-container"></div>`)
	}

	logs := ParseTerminalLogs(payload["logs"])
	html := ToTerminalHTMLFromLogs(logs)
	if html == "" {
		stdout, _ := payload["stdout"].(string)
		stderr, _ := payload["stderr"].(string)
		errorMessage, _ := payload["error_message"].(string)

		displayOutput := stdout + stderr
		if displayOutput == "" {
			displayOutput = errorMessage
		}

		html = ToTerminalHTML(displayOutput)
	}

	terminalID := SanitizeTerminalID(lookupPath)
	html = WrapTerminalLines(html, terminalID)

	status, _ := payload["status"].(string)
	if status == "running" || status == "" {
		return ctx.HTML(http.StatusOK, fmt.Sprintf(
			`<div class="term-container" hx-get="/terminal%s" hx-trigger="load delay:2s" hx-swap="outerHTML">%s</div>`,
			lookupPath,
			html,
		))
	}

	return ctx.HTML(http.StatusOK, fmt.Sprintf(`<div class="term-container">%s</div>`, html))
}

func injectTerminalHTML(node *storage.Tree[storage.Payload], htmlByPath map[string]template.HTML) {
	if node == nil {
		return
	}

	if node.IsLeaf() && node.FullPath != "" {
		if html, ok := htmlByPath[node.FullPath]; ok {
			node.Value["terminalHTML"] = html
		}
	}

	for _, child := range node.Children {
		injectTerminalHTML(child, htmlByPath)
	}
}

// Show handles GET /runs/:id/tasks - Task tree view for a run.
func (c *WebRunsController) Show(ctx *echo.Context) error {
	runID := ctx.Param("id")
	lookupPath := "/pipeline/" + runID + "/"

	results, err := c.store.GetAll(ctx.Request().Context(), lookupPath, []string{"status", "elapsed", "started_at", "usage", "error_message", "error_type"})
	if err != nil {
		return fmt.Errorf("could not get all results: %w", err)
	}

	run, runErr := c.store.GetRun(ctx.Request().Context(), runID)
	isActive := runErr == nil && (run.Status == storage.RunStatusQueued || run.Status == storage.RunStatusRunning)

	var pipeline *storage.Pipeline
	title := "Tasks"
	if runErr == nil && run.PipelineID != "" {
		pipeline, _ = c.store.GetPipeline(ctx.Request().Context(), run.PipelineID)
		if pipeline != nil {
			title = "Tasks \u2014 " + pipeline.Name
		}
	}

	tree := results.AsTree()
	stats := countTaskStats(tree)
	c.preloadTerminalHTML(ctx, lookupPath, tree)

	return ctx.Render(http.StatusOK, "results.html", map[string]any{
		"Tree":     tree,
		"Path":     lookupPath,
		"RunID":    runID,
		"IsActive": isActive,
		"Run":      run,
		"Pipeline": pipeline,
		"Title":    title,
		"Stats":    stats,
	})
}

// Graph handles GET /runs/:id/graph - Task graph view for a run.
func (c *WebRunsController) Graph(ctx *echo.Context) error {
	runID := ctx.Param("id")
	lookupPath := "/pipeline/" + runID + "/"

	results, err := c.store.GetAll(ctx.Request().Context(), lookupPath, []string{"status", "dependsOn"})
	if err != nil {
		return fmt.Errorf("could not get all results: %w", err)
	}

	run, runErr := c.store.GetRun(ctx.Request().Context(), runID)
	isActive := runErr == nil && (run.Status == storage.RunStatusQueued || run.Status == storage.RunStatusRunning)

	var pipeline *storage.Pipeline
	title := "Task Graph"
	if runErr == nil && run.PipelineID != "" {
		pipeline, _ = c.store.GetPipeline(ctx.Request().Context(), run.PipelineID)
		if pipeline != nil {
			title = "Task Graph \u2014 " + pipeline.Name
		}
	}

	tree := results.AsTree()
	treeJSON, err := json.Marshal(tree)
	if err != nil {
		return fmt.Errorf("could not marshal tree: %w", err)
	}

	return ctx.Render(http.StatusOK, "graph.html", map[string]any{
		"Tree":     tree,
		"TreeJSON": string(treeJSON),
		"Path":     lookupPath,
		"RunID":    runID,
		"IsActive": isActive,
		"Run":      run,
		"Pipeline": pipeline,
		"Title":    title,
	})
}

// TasksPartial handles GET /runs/:id/tasks-partial[/] - HTMX partial: tasks container for polling.
func (c *WebRunsController) TasksPartial(ctx *echo.Context) error {
	runID := ctx.Param("id")
	lookupPath := "/pipeline/" + runID + "/"
	q := ctx.QueryParam("q")

	var results storage.Results
	var err error

	if q != "" {
		// Full-text search: return only tasks whose output matches the query.
		// Disable live-polling while a search filter is active.
		results, err = c.store.Search(ctx.Request().Context(), "pipeline/"+runID, q)
		if err != nil {
			return fmt.Errorf("could not search tasks: %w", err)
		}

		tree := results.AsTree()
		stats := countTaskStats(tree)
		c.preloadTerminalHTML(ctx, lookupPath, tree)

		run, runErr := c.store.GetRun(ctx.Request().Context(), runID)
		isActive := runErr == nil && (run.Status == storage.RunStatusQueued || run.Status == storage.RunStatusRunning)

		ctx.Response().Header().Set("HX-Push-Url", fmt.Sprintf("/runs/%s/tasks?q=%s", runID, q))

		return ctx.Render(http.StatusOK, "tasks-partial", map[string]any{
			"Tree":     tree,
			"Path":     lookupPath,
			"RunID":    runID,
			"IsActive": isActive,
			"Run":      run,
			"Stats":    stats,
			"OOB":      true,
		})
	}

	results, err = c.store.GetAll(ctx.Request().Context(), lookupPath, []string{"status", "elapsed", "started_at", "usage", "error_message", "error_type"})
	if err != nil {
		return fmt.Errorf("could not get all results: %w", err)
	}

	run, runErr := c.store.GetRun(ctx.Request().Context(), runID)
	isActive := runErr == nil && (run.Status == storage.RunStatusQueued || run.Status == storage.RunStatusRunning)

	tree := results.AsTree()
	stats := countTaskStats(tree)
	c.preloadTerminalHTML(ctx, lookupPath, tree)

	// Return 286 to signal htmx to stop polling when the run is no longer active.
	statusCode := http.StatusOK
	if !isActive {
		statusCode = 286
	}

	return ctx.Render(statusCode, "tasks-partial", map[string]any{
		"Tree":     tree,
		"Path":     lookupPath,
		"RunID":    runID,
		"IsActive": isActive,
		"Run":      run,
		"Stats":    stats,
		"OOB":      true,
	})
}

// GraphData handles GET /runs/:id/graph-data[/] - HTMX partial: graph data JSON for polling.
func (c *WebRunsController) GraphData(ctx *echo.Context) error {
	runID := ctx.Param("id")
	lookupPath := "/pipeline/" + runID + "/"

	results, err := c.store.GetAll(ctx.Request().Context(), lookupPath, []string{"status", "dependsOn"})
	if err != nil {
		return fmt.Errorf("could not get all results: %w", err)
	}

	run, runErr := c.store.GetRun(ctx.Request().Context(), runID)
	isActive := runErr == nil && (run.Status == storage.RunStatusQueued || run.Status == storage.RunStatusRunning)

	tree := results.AsTree()
	treeJSON, err := json.Marshal(tree)
	if err != nil {
		return fmt.Errorf("could not marshal tree: %w", err)
	}

	// Return 286 to signal htmx to stop polling when the run is no longer active.
	statusCode := http.StatusOK
	if !isActive {
		statusCode = 286
	}

	return ctx.Render(statusCode, "graph-partial", map[string]any{
		"Tree":     tree,
		"TreeJSON": string(treeJSON),
		"Path":     lookupPath,
		"RunID":    runID,
		"IsActive": isActive,
		"Run":      run,
		"OOB":      true,
	})
}

// RegisterRoutes registers all run web view routes on the given group.
func (c *WebRunsController) RegisterRoutes(web *echo.Group) {
	web.GET("/runs/:id/tasks", c.Show)
	web.GET("/runs/:id/graph", c.Graph)
	web.GET("/runs/:id/tasks-partial", c.TasksPartial)
	web.GET("/runs/:id/tasks-partial/", c.TasksPartial)
	web.GET("/runs/:id/graph-data", c.GraphData)
	web.GET("/runs/:id/graph-data/", c.GraphData)
	web.GET("/terminal/*", c.Terminal)
}
