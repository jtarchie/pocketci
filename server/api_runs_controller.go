package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// APIRunsController handles JSON API endpoints for pipeline runs.
type APIRunsController struct {
	BaseController
	allowedFeatures []Feature
}

type APIRunTask struct {
	Path    string          `json:"path"`
	Payload storage.Payload `json:"payload"`
}

func normalizeRunTaskPath(path, runPrefix string) string {
	start := strings.Index(path, runPrefix)
	if start >= 0 {
		return path[start:]
	}

	return path
}

// Status handles GET /api/runs/:run_id/status - Get run status.
func (c *APIRunsController) Status(ctx *echo.Context) error {
	runID := ctx.Param("run_id")

	run, err := c.store.GetRun(ctx.Request().Context(), runID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			nfJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "run not found",
			})
			if nfJsonErr != nil {
				return fmt.Errorf("run status not found response: %w", nfJsonErr)
			}

			return nil
		}

		errJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get run: %v", err),
		})
		if errJsonErr != nil {
			return fmt.Errorf("run status error response: %w", errJsonErr)
		}

		return nil
	}

	okJsonErr := ctx.JSON(http.StatusOK, run)
	if okJsonErr != nil {
		return fmt.Errorf("run status response: %w", okJsonErr)
	}

	return nil
}

// Tasks handles GET /api/runs/:run_id/tasks - List run tasks with payload data.
func (c *APIRunsController) Tasks(ctx *echo.Context) error {
	runID := ctx.Param("run_id")

	_, err := c.store.GetRun(ctx.Request().Context(), runID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			tNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "run not found",
			})
			if tNFJsonErr != nil {
				return fmt.Errorf("tasks run not found response: %w", tNFJsonErr)
			}

			return nil
		}

		tErrJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get run: %v", err),
		})
		if tErrJsonErr != nil {
			return fmt.Errorf("tasks run error response: %w", tErrJsonErr)
		}

		return nil
	}

	prefix := "/pipeline/" + runID + "/"
	taskPath := ctx.QueryParam("path")

	if taskPath != "" {
		return c.tasksByPath(ctx, prefix, taskPath)
	}

	results, err := c.store.GetAll(ctx.Request().Context(), prefix, []string{"*"})
	if err != nil {
		gaErrJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get tasks: %v", err),
		})
		if gaErrJsonErr != nil {
			return fmt.Errorf("tasks get all error response: %w", gaErrJsonErr)
		}

		return nil
	}

	response := make([]APIRunTask, 0, len(results))
	for _, result := range results {
		response = append(response, APIRunTask{Path: normalizeRunTaskPath(result.Path, prefix), Payload: result.Payload})
	}

	listJsonErr := ctx.JSON(http.StatusOK, response)
	if listJsonErr != nil {
		return fmt.Errorf("tasks list response: %w", listJsonErr)
	}

	return nil
}

// tasksByPath fetches a single task by its path, scoped to the run prefix.
func (c *APIRunsController) tasksByPath(ctx *echo.Context, prefix, taskPath string) error {
	lookupPath := taskPath
	if !strings.HasPrefix(taskPath, "/") {
		lookupPath = prefix + strings.TrimPrefix(taskPath, "/")
	}

	if !strings.HasPrefix(lookupPath, prefix) {
		badReqJsonErr := ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "task path must be scoped to the run",
		})
		if badReqJsonErr != nil {
			return fmt.Errorf("tasks bad request response: %w", badReqJsonErr)
		}

		return nil
	}

	payload, getErr := c.store.Get(ctx.Request().Context(), lookupPath)
	if getErr != nil {
		if errors.Is(getErr, storage.ErrNotFound) {
			tpNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "task not found",
			})
			if tpNFJsonErr != nil {
				return fmt.Errorf("tasks task not found response: %w", tpNFJsonErr)
			}

			return nil
		}

		tpErrJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get task: %v", getErr),
		})
		if tpErrJsonErr != nil {
			return fmt.Errorf("tasks get task error response: %w", tpErrJsonErr)
		}

		return nil
	}

	singleJsonErr := ctx.JSON(http.StatusOK, []APIRunTask{{Path: lookupPath, Payload: payload}})
	if singleJsonErr != nil {
		return fmt.Errorf("tasks single task response: %w", singleJsonErr)
	}

	return nil
}

// Stop handles POST /api/runs/:run_id/stop - Stop a running pipeline run.
func (c *APIRunsController) Stop(ctx *echo.Context) error {
	runID := ctx.Param("run_id")

	run, err := c.store.GetRun(ctx.Request().Context(), runID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			stopNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "run not found",
			})
			if stopNFJsonErr != nil {
				return fmt.Errorf("stop run not found response: %w", stopNFJsonErr)
			}

			return nil
		}

		stopErrJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get run: %v", err),
		})
		if stopErrJsonErr != nil {
			return fmt.Errorf("stop run error response: %w", stopErrJsonErr)
		}

		return nil
	}

	if run.Status != storage.RunStatusRunning && run.Status != storage.RunStatusQueued {
		conflictJsonErr := ctx.JSON(http.StatusConflict, map[string]string{
			"error": "run is not currently in flight",
		})
		if conflictJsonErr != nil {
			return fmt.Errorf("stop run conflict response: %w", conflictJsonErr)
		}

		return nil
	}

	stopErr := c.execService.StopRun(runID)
	if stopErr != nil {
		if errors.Is(stopErr, ErrRunNotInFlight) {
			return c.stopOrphanedRun(ctx, runID)
		}

		stopInternalJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to stop run: %v", stopErr),
		})
		if stopInternalJsonErr != nil {
			return fmt.Errorf("stop run internal error response: %w", stopInternalJsonErr)
		}

		return nil
	}

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Trigger", `{"showToast":{"message":"Stopping run...","type":"success"}}`)

		stopNoContentErr := ctx.NoContent(http.StatusOK)
		if stopNoContentErr != nil {
			return fmt.Errorf("stop run stopping response: %w", stopNoContentErr)
		}

		return nil
	}

	stopOkJsonErr := ctx.JSON(http.StatusOK, map[string]string{
		"run_id": runID,
		"status": "stopping",
	})
	if stopOkJsonErr != nil {
		return fmt.Errorf("stop run stopping ok response: %w", stopOkJsonErr)
	}

	return nil
}

// stopOrphanedRun handles the case where a run appears running/queued in the DB
// but has no active goroutine (e.g. the server crashed and restarted).
// It forces the run to failed and returns the appropriate HTTP response.
func (c *APIRunsController) stopOrphanedRun(ctx *echo.Context, runID string) error {
	reqCtx := ctx.Request().Context()
	_ = c.store.UpdateStatusForPrefix(reqCtx, "/pipeline/"+runID+"/", []string{"pending", "running"}, "aborted")

	updateErr := c.store.UpdateRunStatus(reqCtx, runID, storage.RunStatusFailed, "Run stopped by user")
	if updateErr != nil {
		orphanErrJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to mark orphaned run as failed: %v", updateErr),
		})
		if orphanErrJsonErr != nil {
			return fmt.Errorf("stop run orphan error response: %w", orphanErrJsonErr)
		}

		return nil
	}

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Trigger", `{"showToast":{"message":"Run stopped","type":"success"}}`)

		orphanNoContentErr := ctx.NoContent(http.StatusOK)
		if orphanNoContentErr != nil {
			return fmt.Errorf("stop run stopped response: %w", orphanNoContentErr)
		}

		return nil
	}

	orphanOkJsonErr := ctx.JSON(http.StatusOK, map[string]string{
		"run_id": runID,
		"status": "stopped",
	})
	if orphanOkJsonErr != nil {
		return fmt.Errorf("stop run stopped ok response: %w", orphanOkJsonErr)
	}

	return nil
}

// resumeValidate fetches and validates the run for resumption. It returns the
// run and its pipeline, or writes an error response and returns a non-nil error.
func (c *APIRunsController) resumeValidate(ctx *echo.Context, runID string) (*storage.PipelineRun, *storage.Pipeline, error) {
	reqCtx := ctx.Request().Context()

	run, err := c.store.GetRun(reqCtx, runID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			rvNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "run not found",
			})
			if rvNFJsonErr != nil {
				return nil, nil, fmt.Errorf("resume run not found response: %w", rvNFJsonErr)
			}

			return nil, nil, errHandled
		}

		rvErrJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get run: %v", err),
		})
		if rvErrJsonErr != nil {
			return nil, nil, fmt.Errorf("resume run error response: %w", rvErrJsonErr)
		}

		return nil, nil, errHandled
	}

	if run.Status != storage.RunStatusFailed {
		rvConflictJsonErr := ctx.JSON(http.StatusConflict, map[string]string{
			"error": "only failed runs can be resumed",
		})
		if rvConflictJsonErr != nil {
			return nil, nil, fmt.Errorf("resume run conflict response: %w", rvConflictJsonErr)
		}

		return nil, nil, errHandled
	}

	pipeline, err := c.store.GetPipeline(reqCtx, run.PipelineID)
	if err != nil {
		rvPipelineJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
		if rvPipelineJsonErr != nil {
			return nil, nil, fmt.Errorf("resume get pipeline error response: %w", rvPipelineJsonErr)
		}

		return nil, nil, errHandled
	}

	return run, pipeline, nil
}

// Resume handles POST /api/runs/:run_id/resume - Resume a failed or aborted run.
func (c *APIRunsController) Resume(ctx *echo.Context) error {
	if !IsFeatureEnabled(FeatureResume, c.allowedFeatures) {
		featJsonErr := ctx.JSON(http.StatusForbidden, map[string]string{
			"error": "resume feature is not enabled",
		})
		if featJsonErr != nil {
			return fmt.Errorf("resume feature disabled response: %w", featJsonErr)
		}

		return nil
	}

	runID := ctx.Param("run_id")

	run, pipeline, err := c.resumeValidate(ctx, runID)
	if err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	resumeErr := c.execService.ResumePipeline(ctx.Request().Context(), pipeline, run)
	if resumeErr != nil {
		if errors.Is(resumeErr, ErrQueueFull) {
			return c.respondQueueFull(ctx, "resume queue full response", "resume queue full json response")
		}

		resumeInternalJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to resume run: %v", resumeErr),
		})
		if resumeInternalJsonErr != nil {
			return fmt.Errorf("resume internal error response: %w", resumeInternalJsonErr)
		}

		return nil
	}

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Trigger", `{"showToast":{"message":"Resuming run...","type":"success"}}`)

		resumeNoContentErr := ctx.NoContent(http.StatusOK)
		if resumeNoContentErr != nil {
			return fmt.Errorf("resume ok response: %w", resumeNoContentErr)
		}

		return nil
	}

	resumeOkJsonErr := ctx.JSON(http.StatusOK, map[string]string{
		"run_id": runID,
		"status": "resuming",
	})
	if resumeOkJsonErr != nil {
		return fmt.Errorf("resume resuming response: %w", resumeOkJsonErr)
	}

	return nil
}

// respondQueueFull writes an HTTP 429 response appropriate for the request type
// (HTMX text or JSON). The two error context strings are used in error wrapping.
func (c *APIRunsController) respondQueueFull(ctx *echo.Context, htmxErrCtx, jsonErrCtx string) error {
	if isHtmxRequest(ctx) {
		qfStrErr := ctx.String(http.StatusTooManyRequests, "Execution queue is full")
		if qfStrErr != nil {
			return fmt.Errorf("%s: %w", htmxErrCtx, qfStrErr)
		}

		return nil
	}

	qfJsonErr := ctx.JSON(http.StatusTooManyRequests, map[string]any{
		"error":          "execution queue is full",
		"in_flight":      c.execService.CurrentInFlight(),
		"max_in_flight":  c.execService.MaxInFlight(),
		"max_queue_size": c.execService.MaxQueueSize(),
	})
	if qfJsonErr != nil {
		return fmt.Errorf("%s: %w", jsonErrCtx, qfJsonErr)
	}

	return nil
}

// RegisterRoutes registers all run API routes on the given group.
func (c *APIRunsController) RegisterRoutes(api *echo.Group) {
	api.GET("/runs/:run_id/status", c.Status)
	api.GET("/runs/:run_id/tasks", c.Tasks)
	api.POST("/runs/:run_id/stop", c.Stop)
	api.POST("/runs/:run_id/resume", c.Resume)
}
