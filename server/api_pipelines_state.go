package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// Pause handles POST /api/pipelines/:id/pause - Pause a pipeline.
func (c *APIPipelinesController) Pause(ctx *echo.Context) error {
	return c.setPaused(ctx, true)
}

// Unpause handles POST /api/pipelines/:id/unpause - Unpause a pipeline.
func (c *APIPipelinesController) Unpause(ctx *echo.Context) error {
	return c.setPaused(ctx, false)
}

// setPausedNotFound writes an appropriate 404 response for HTMX or JSON clients.
func (c *APIPipelinesController) setPausedNotFound(ctx *echo.Context) error {
	return respondHTMXOrJSON(ctx, http.StatusNotFound,
		"set paused not found", "Pipeline not found",
		map[string]string{"error": "pipeline not found"})
}

func (c *APIPipelinesController) setPaused(ctx *echo.Context, paused bool) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return c.setPausedNotFound(ctx)
		}

		spJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if spJsonErr != nil {
			return fmt.Errorf("set paused error response: %w", spJsonErr)
		}

		return nil
	}

	spRbacErr := checkPipelineRBAC(ctx, pipeline)
	if spRbacErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	updateErr := c.store.UpdatePipeline(ctx.Request().Context(), id, storage.PipelineUpdate{Paused: &paused})
	if updateErr != nil {
		upJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if upJsonErr != nil {
			return fmt.Errorf("set paused update error response: %w", upJsonErr)
		}

		return nil
	}

	action := "paused"
	if !paused {
		action = "unpaused"
	}

	c.logger.Info("pipeline."+action,
		slog.String("pipeline", pipeline.Name),
		slog.String("pipeline_id", pipeline.ID),
		slog.String("actor", formatActor(ctx)),
	)

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":{"message":"%s %s successfully","type":"success"}}`, pipeline.Name, action))
		ctx.Response().Header().Set("HX-Refresh", "true")

		htmxNoContentErr := ctx.NoContent(http.StatusOK)
		if htmxNoContentErr != nil {
			return fmt.Errorf("set paused ok response: %w", htmxNoContentErr)
		}

		return nil
	}

	spOkJsonErr := ctx.JSON(http.StatusOK, map[string]any{
		"id":      pipeline.ID,
		"paused":  paused,
		"message": "pipeline " + action,
	})
	if spOkJsonErr != nil {
		return fmt.Errorf("set paused response: %w", spOkJsonErr)
	}

	return nil
}

// SeedJobPassed handles POST /api/pipelines/:id/jobs/:name/seed-passed.
// It creates a synthetic success record so that cross-run `passed` constraints
// referencing the named job are immediately satisfied.
func (c *APIPipelinesController) SeedJobPassed(ctx *echo.Context) error {
	pipelineID := ctx.Param("id")
	jobName := ctx.Param("name")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), pipelineID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			seedNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
			if seedNFJsonErr != nil {
				return fmt.Errorf("seed job not found response: %w", seedNFJsonErr)
			}

			return nil
		}

		seedGetJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if seedGetJsonErr != nil {
			return fmt.Errorf("seed job get error response: %w", seedGetJsonErr)
		}

		return nil
	}

	seedRbacErr := checkPipelineRBAC(ctx, pipeline)
	if seedRbacErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	// Create a synthetic run to hold the seeded record
	run, err := c.store.SaveRun(
		ctx.Request().Context(),
		pipeline.ID,
		"seed",
		formatActor(ctx),
		storage.TriggerInput{},
		"",
	)
	if err != nil {
		saveRunJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if saveRunJsonErr != nil {
			return fmt.Errorf("seed job save run error response: %w", saveRunJsonErr)
		}

		return nil
	}

	// Write the success task record at the standard path
	taskPath := "/pipeline/" + run.ID + "/jobs/" + jobName
	setErr := c.store.Set(ctx.Request().Context(), taskPath, map[string]any{
		"status": "success",
		"seeded": true,
	})
	if setErr != nil {
		setJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if setJsonErr != nil {
			return fmt.Errorf("seed job set error response: %w", setJsonErr)
		}

		return nil
	}

	// Immediately mark the run as completed
	updateRunErr := c.store.UpdateRunStatus(ctx.Request().Context(), run.ID, storage.RunStatusSuccess, "")
	if updateRunErr != nil {
		c.logger.Error("seed.run.update.failed", "error", updateRunErr)
	}

	seedOkJsonErr := ctx.JSON(http.StatusOK, map[string]any{
		"pipeline_id": pipeline.ID,
		"job":         jobName,
		"run_id":      run.ID,
		"message":     "job passed status seeded successfully",
	})
	if seedOkJsonErr != nil {
		return fmt.Errorf("seed job response: %w", seedOkJsonErr)
	}

	return nil
}
