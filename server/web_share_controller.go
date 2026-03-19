package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// WebShareController handles public (unauthenticated) shared run views.
// Each route validates the share token before serving data.
type WebShareController struct {
	WebRunsController
	secretsMgr    secrets.Manager
	signingSecret string
	logger        *slog.Logger
}

// validateToken extracts the runID from a share token, returning 404 on any
// failure to avoid revealing whether a run exists.
func (c *WebShareController) validateToken(ctx *echo.Context) (runID string, err error) {
	token := ctx.Param("token")

	claims, err := auth.ValidateShareToken(token, c.signingSecret)
	if err != nil {
		c.logger.Debug("share.token.invalid", slog.String("error", err.Error()))

		return "", echo.NewHTTPError(http.StatusNotFound, "not found")
	}

	return claims.RunID, nil
}

// Show handles GET /share/:token/tasks — a fully static, read-only run view.
// All terminal HTML is preloaded server-side with secrets redacted.
func (c *WebShareController) Show(ctx *echo.Context) error {
	runID, err := c.validateToken(ctx)
	if err != nil {
		return err
	}

	lookupPath := "/pipeline/" + runID + "/"

	results, err := c.store.GetAll(ctx.Request().Context(), lookupPath, []string{"status", "elapsed", "started_at", "usage", "error_message", "error_type"})
	if err != nil {
		return fmt.Errorf("could not get all results: %w", err)
	}

	run, runErr := c.store.GetRun(ctx.Request().Context(), runID)

	var pipeline *storage.Pipeline
	title := "Tasks"
	var pipelineID string

	if runErr == nil && run.PipelineID != "" {
		pipelineID = run.PipelineID
		pipeline, _ = c.store.GetPipeline(ctx.Request().Context(), run.PipelineID)
		if pipeline != nil {
			title = "Tasks \u2014 " + pipeline.Name
		}
	}

	tree := results.AsTree()
	stats := countTaskStats(tree)

	// Collect secret values for redaction and preload terminal HTML statically.
	secretValues := collectSecretValues(ctx.Request().Context(), c.secretsMgr, pipelineID)
	redact := func(html string) string {
		return support.RedactSecrets(html, secretValues)
	}

	// Force all tasks to be treated as non-running so no HTMX reload attributes
	// are injected into terminal HTML — the shared view is always static.
	c.preloadTerminalHTMLWithOptions(ctx, lookupPath, tree, "/terminal", redact)

	return ctx.Render(http.StatusOK, "results.html", map[string]any{
		"Tree":     tree,
		"Path":     lookupPath,
		"RunID":    runID,
		"IsActive": false,
		"Run":      run,
		"Pipeline": pipeline,
		"Title":    title,
		"Stats":    stats,
		"ReadOnly": true,
	})
}

// RegisterRoutes registers public share routes on the main (unauthenticated) router.
func (c *WebShareController) RegisterRoutes(router *echo.Echo) {
	router.GET("/share/:token/tasks", c.Show)
}
