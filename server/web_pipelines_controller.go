package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// PipelineRow is a view model for the pipeline listing page that pairs a
// pipeline with its most recent run (nil if no runs exist yet).
type PipelineRow struct {
	Pipeline   storage.Pipeline
	LatestRun  *storage.PipelineRun
	DriverName string
}

// buildPipelineRows fetches the latest run for each pipeline and returns
// a slice of PipelineRow view models ready for the template.
func buildPipelineRows(ctx context.Context, store storage.Driver, pipelines []storage.Pipeline, defaultDriver string) []PipelineRow {
	rows := make([]PipelineRow, 0, len(pipelines))
	for _, p := range pipelines {
		driverName := p.Driver
		if driverName == "" {
			if defaultDriver != "" {
				driverName = defaultDriver
			} else {
				driverName = "default"
			}
		}
		row := PipelineRow{Pipeline: p, DriverName: driverName}
		if res, err := store.SearchRunsByPipeline(ctx, p.ID, "", 1, 1); err == nil && len(res.Items) > 0 {
			row.LatestRun = &res.Items[0]
		}
		rows = append(rows, row)
	}
	return rows
}

// WebPipelinesController handles HTML view endpoints for pipelines.
type WebPipelinesController struct {
	BaseController
}

// filterRowsByRBAC removes pipeline rows the current user is not allowed to see.
func filterRowsByRBAC(ctx *echo.Context, rows []PipelineRow) []PipelineRow {
	user := auth.GetUser(ctx)

	filtered := rows[:0]
	for _, row := range rows {
		if row.Pipeline.RBACExpression != "" {
			// No OAuth user means RBAC cannot be evaluated — hide the pipeline.
			if user == nil {
				continue
			}

			allowed, err := auth.EvaluateAccess(row.Pipeline.RBACExpression, *user)
			if err != nil || !allowed {
				continue
			}
		}
		filtered = append(filtered, row)
	}

	return filtered
}

// hasActiveRunInRows returns true if any row has a running or queued latest run.
func hasActiveRunInRows(rows []PipelineRow) bool {
	for _, row := range rows {
		if row.LatestRun != nil && (row.LatestRun.Status == storage.RunStatusRunning || row.LatestRun.Status == storage.RunStatusQueued) {
			return true
		}
	}

	return false
}

// Index handles GET /pipelines/ - Pipeline listing page.
// Returns full HTML page for normal requests, or just the pipelines-content
// partial for HTMX requests (search, pagination, polling).
func (c *WebPipelinesController) Index(ctx *echo.Context) error {
	q := ctx.QueryParam("q")

	page := 1
	perPage := 20

	if p := ctx.QueryParam("page"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &page)
	}
	if pp := ctx.QueryParam("per_page"); pp != "" {
		_, _ = fmt.Sscanf(pp, "%d", &perPage)
	}

	result, err := c.store.SearchPipelines(ctx.Request().Context(), q, page, perPage)
	if err != nil {
		return fmt.Errorf("could not list pipelines: %w", err)
	}

	if result == nil || result.Items == nil {
		result = &storage.PaginationResult[storage.Pipeline]{
			Items:      []storage.Pipeline{},
			Page:       page,
			PerPage:    perPage,
			TotalItems: 0,
			TotalPages: 0,
			HasNext:    false,
		}
	}

	rows := buildPipelineRows(ctx.Request().Context(), c.store, result.Items, c.execService.DefaultDriver)
	rows = filterRowsByRBAC(ctx, rows)

	data := map[string]any{
		"PipelineRows":  rows,
		"Pagination":    result,
		"Query":         q,
		"HasActiveRuns": hasActiveRunInRows(rows),
	}

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Push-Url", buildPipelinesURL(q, page, perPage))

		return ctx.Render(http.StatusOK, "pipelines-content", data)
	}

	return ctx.Render(http.StatusOK, "pipelines.html", data)
}

// Show handles GET /pipelines/:id/ - Pipeline detail page.
func (c *WebPipelinesController) Show(ctx *echo.Context) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.String(http.StatusNotFound, "Pipeline not found")
		}
		return fmt.Errorf("could not get pipeline: %w", err)
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return err
	}

	page := 1
	perPage := 20

	if p := ctx.QueryParam("page"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &page)
	}
	if pp := ctx.QueryParam("per_page"); pp != "" {
		_, _ = fmt.Sscanf(pp, "%d", &perPage)
	}

	q := ctx.QueryParam("q")

	var result *storage.PaginationResult[storage.PipelineRun]
	var runsErr error
	if q != "" {
		result, runsErr = c.store.SearchRunsByPipeline(ctx.Request().Context(), id, q, page, perPage)
	} else {
		result, runsErr = c.store.SearchRunsByPipeline(ctx.Request().Context(), id, "", page, perPage)
	}
	if runsErr != nil {
		return fmt.Errorf("could not list runs: %w", runsErr)
	}

	if result == nil || result.Items == nil {
		result = &storage.PaginationResult[storage.PipelineRun]{
			Items:      []storage.PipelineRun{},
			Page:       page,
			PerPage:    perPage,
			TotalItems: 0,
			TotalPages: 0,
			HasNext:    false,
		}
	}

	driverName := pipeline.Driver
	if driverName == "" {
		driverName = c.execService.DefaultDriver
	}
	return ctx.Render(http.StatusOK, "pipeline_detail.html", map[string]any{
		"Pipeline":   pipeline,
		"DriverName": driverName,
		"Runs":       result.Items,
		"Pagination": result,
		"Query":      q,
	})
}

// RunsSection handles GET /pipelines/:id/runs-section[/] - HTMX partial: runs section for a pipeline.
func (c *WebPipelinesController) RunsSection(ctx *echo.Context) error {
	id := ctx.Param("id")

	page := 1
	perPage := 20

	if p := ctx.QueryParam("page"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &page)
	}
	if pp := ctx.QueryParam("per_page"); pp != "" {
		_, _ = fmt.Sscanf(pp, "%d", &perPage)
	}

	result, err := c.store.SearchRunsByPipeline(ctx.Request().Context(), id, "", page, perPage)
	if err != nil {
		return fmt.Errorf("could not list runs: %w", err)
	}

	if result == nil || result.Items == nil {
		result = &storage.PaginationResult[storage.PipelineRun]{
			Items:      []storage.PipelineRun{},
			Page:       page,
			PerPage:    perPage,
			TotalItems: 0,
			TotalPages: 0,
			HasNext:    false,
		}
	}

	return ctx.Render(http.StatusOK, "runs-section", map[string]any{
		"PipelineID": id,
		"Runs":       result.Items,
		"Pagination": result,
		"Query":      "",
	})
}

// RunsSearch handles GET /pipelines/:id/runs-search[/] - HTMX partial: runs-section filtered by ?q=.
func (c *WebPipelinesController) RunsSearch(ctx *echo.Context) error {
	id := ctx.Param("id")
	q := ctx.QueryParam("q")

	page := 1
	perPage := 20

	if p := ctx.QueryParam("page"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &page)
	}
	if pp := ctx.QueryParam("per_page"); pp != "" {
		_, _ = fmt.Sscanf(pp, "%d", &perPage)
	}

	result, err := c.store.SearchRunsByPipeline(ctx.Request().Context(), id, q, page, perPage)
	if err != nil {
		return fmt.Errorf("could not search runs: %w", err)
	}

	if result == nil || result.Items == nil {
		result = &storage.PaginationResult[storage.PipelineRun]{
			Items:      []storage.PipelineRun{},
			Page:       page,
			PerPage:    perPage,
			TotalItems: 0,
			TotalPages: 0,
			HasNext:    false,
		}
	}

	ctx.Response().Header().Set("HX-Push-Url", fmt.Sprintf("/pipelines/%s/?q=%s&page=%d&per_page=%d", id, q, page, perPage))

	return ctx.Render(http.StatusOK, "runs-section", map[string]any{
		"PipelineID": id,
		"Runs":       result.Items,
		"Pagination": result,
		"Query":      q,
	})
}

// Source handles GET /pipelines/:id/source[/] - Pipeline source view.
func (c *WebPipelinesController) Source(ctx *echo.Context) error {
	id := ctx.Param("id")
	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.String(http.StatusNotFound, "Pipeline not found")
		}
		return fmt.Errorf("could not get pipeline: %w", err)
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return err
	}

	return ctx.Render(http.StatusOK, "pipeline_source.html", map[string]any{
		"Pipeline": pipeline,
	})
}

// buildPipelinesURL constructs a URL for the pipelines listing page.
func buildPipelinesURL(q string, page, perPage int) string {
	url := "/pipelines/"
	if q != "" || page > 1 || perPage != 20 {
		url += fmt.Sprintf("?q=%s&page=%d&per_page=%d", q, page, perPage)
	}

	return url
}

// RegisterRoutes registers all pipeline web view routes on the given group.
func (c *WebPipelinesController) RegisterRoutes(web *echo.Group) {
	web.GET("/pipelines/", c.Index)
	web.GET("/pipelines/:id/", c.Show)
	web.GET("/pipelines/:id/runs-section", c.RunsSection)
	web.GET("/pipelines/:id/runs-section/", c.RunsSection)
	web.GET("/pipelines/:id/runs-search", c.RunsSearch)
	web.GET("/pipelines/:id/runs-search/", c.RunsSearch)
	web.GET("/pipelines/:id/source", c.Source)
	web.GET("/pipelines/:id/source/", c.Source)
}
