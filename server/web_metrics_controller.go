package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// PipelineMetrics holds computed metrics for a single pipeline.
type PipelineMetrics struct {
	Pipeline    storage.Pipeline
	TotalRuns   int
	SuccessRuns int
	FailedRuns  int
	SkippedRuns int
	AvgDuration time.Duration // average of completed runs with both timestamps; 0 if none
	LastRun     *storage.PipelineRun
}

// SuccessRate returns the success percentage (0–100) among completed runs.
func (p PipelineMetrics) SuccessRate() int {
	completed := p.SuccessRuns + p.FailedRuns
	if completed == 0 {
		return 0
	}
	return (p.SuccessRuns * 100) / completed
}

// AvgDurationStr returns a human-readable average duration, or "—" if unavailable.
func (p PipelineMetrics) AvgDurationStr() string {
	if p.AvgDuration == 0 {
		return "—"
	}
	d := p.AvgDuration.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// MetricsDashboardData is the view model passed to the metrics templates.
type MetricsDashboardData struct {
	// System overview
	TotalPipelines int
	TotalRuns      int
	InFlight       int
	MaxInFlight    int

	// Run counts by status
	Queued  int
	Running int
	Success int
	Failed  int
	Skipped int

	// Per-pipeline stats
	PipelineMetrics []PipelineMetrics

	// Recent failures (latest 10)
	RecentFailures []FailedRunRow

	// Pre-computed bar widths (0-100 integers) to avoid template arithmetic
	InFlightPct int
	SuccessPct  int
	FailedPct   int
	SkippedPct  int
	RunningPct  int
	QueuedPct   int
}

// FailedRunRow pairs a failed run with its pipeline name for display.
type FailedRunRow struct {
	Run          storage.PipelineRun
	PipelineName string
}

// OverallSuccessRate returns the overall success percentage across all completed runs.
func (d MetricsDashboardData) OverallSuccessRate() int {
	completed := d.Success + d.Failed
	if completed == 0 {
		return 0
	}
	return (d.Success * 100) / completed
}

// WebMetricsController handles the /metrics/ dashboard page.
type WebMetricsController struct {
	BaseController
}

func (c *WebMetricsController) RegisterRoutes(web *echo.Group) {
	web.GET("/metrics/", c.Index)
	web.GET("/metrics/content", c.Content)
}

// Index handles GET /metrics/ — returns the full page.
func (c *WebMetricsController) Index(ctx *echo.Context) error {
	data, err := c.buildData(ctx)
	if err != nil {
		return err
	}

	return ctx.Render(http.StatusOK, "metrics.html", data)
}

// Content handles GET /metrics/content — HTMX partial for auto-refresh.
func (c *WebMetricsController) Content(ctx *echo.Context) error {
	data, err := c.buildData(ctx)
	if err != nil {
		return err
	}

	return ctx.Render(http.StatusOK, "metrics-content", data)
}

func (c *WebMetricsController) buildData(ctx *echo.Context) (MetricsDashboardData, error) {
	reqCtx := ctx.Request().Context()

	var data MetricsDashboardData

	// In-flight from execution service (in-memory, no DB needed)
	data.InFlight = c.execService.CurrentInFlight()
	data.MaxInFlight = c.execService.MaxInFlight()

	// Run counts grouped by status
	stats, err := c.store.GetRunStats(reqCtx)
	if err != nil {
		return data, err
	}
	data.Queued = stats[storage.RunStatusQueued]
	data.Running = stats[storage.RunStatusRunning]
	data.Success = stats[storage.RunStatusSuccess]
	data.Failed = stats[storage.RunStatusFailed]
	data.Skipped = stats[storage.RunStatusSkipped]
	for _, count := range stats {
		data.TotalRuns += count
	}

	// Total number of pipelines
	result, err := c.store.SearchPipelines(reqCtx, "", 1, 1)
	if err != nil {
		return data, err
	}
	data.TotalPipelines = result.TotalItems

	// Per-pipeline metrics — fetch all pipelines (up to 200 for the dashboard)
	allPipelines, err := c.store.SearchPipelines(reqCtx, "", 1, 200)
	if err != nil {
		return data, err
	}
	data.PipelineMetrics = make([]PipelineMetrics, 0, len(allPipelines.Items))
	for _, pipeline := range allPipelines.Items {
		pm := computePipelineMetrics(reqCtx, c.store, pipeline)
		data.PipelineMetrics = append(data.PipelineMetrics, pm)
	}

	// Recent failures — build pipeline name index from what we already have
	nameByID := make(map[string]string, len(allPipelines.Items))
	for _, p := range allPipelines.Items {
		nameByID[p.ID] = p.Name
	}

	recentFailed, err := c.store.GetRunsByStatus(reqCtx, storage.RunStatusFailed, 10)
	if err != nil {
		return data, err
	}
	data.RecentFailures = make([]FailedRunRow, 0, len(recentFailed))
	for _, run := range recentFailed {
		name := nameByID[run.PipelineID]
		if name == "" {
			// Pipeline not in first 200 — do a point lookup
			if p, pErr := c.store.GetPipeline(reqCtx, run.PipelineID); pErr == nil {
				name = p.Name
			}
		}
		data.RecentFailures = append(data.RecentFailures, FailedRunRow{Run: run, PipelineName: name})
	}

	// Pre-compute percentages for use in templates (avoid template arithmetic)
	if data.MaxInFlight > 0 {
		data.InFlightPct = pct(data.InFlight, data.MaxInFlight)
	}
	if data.TotalRuns > 0 {
		data.SuccessPct = pct(data.Success, data.TotalRuns)
		data.FailedPct = pct(data.Failed, data.TotalRuns)
		data.SkippedPct = pct(data.Skipped, data.TotalRuns)
		data.RunningPct = pct(data.Running, data.TotalRuns)
		data.QueuedPct = pct(data.Queued, data.TotalRuns)
	}

	return data, nil
}

func pct(part, total int) int {
	if total == 0 {
		return 0
	}
	return (part * 100) / total
}

// computePipelineMetrics fetches recent runs for a pipeline and computes
// success/failure counts, average duration, and last run.
func computePipelineMetrics(ctx context.Context, store storage.Driver, pipeline storage.Pipeline) PipelineMetrics {
	pm := PipelineMetrics{Pipeline: pipeline}

	runs, err := store.SearchRunsByPipeline(ctx, pipeline.ID, "", 1, 100)
	if err != nil || runs == nil {
		return pm
	}

	pm.TotalRuns = runs.TotalItems

	var durationSum time.Duration
	var durationCount int

	for i := range runs.Items {
		run := &runs.Items[i]
		switch run.Status {
		case storage.RunStatusSuccess:
			pm.SuccessRuns++
		case storage.RunStatusFailed:
			pm.FailedRuns++
		case storage.RunStatusSkipped:
			pm.SkippedRuns++
		case storage.RunStatusQueued, storage.RunStatusRunning:
			// not yet complete — not counted in summary metrics
		}
		if run.StartedAt != nil && run.CompletedAt != nil {
			durationSum += run.CompletedAt.Sub(*run.StartedAt)
			durationCount++
		}
		if pm.LastRun == nil {
			pm.LastRun = run
		}
	}

	if durationCount > 0 {
		pm.AvgDuration = durationSum / time.Duration(durationCount)
	}

	return pm
}
