package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// PipelineRequest represents the JSON body for creating or updating a pipeline.
type PipelineRequest struct {
	Content        string            `json:"content"`
	ContentType    string            `json:"content_type"`
	Driver         string            `json:"driver"`
	DriverConfig   json.RawMessage   `json:"driver_config,omitempty"`
	WebhookSecret  *string           `json:"webhook_secret,omitempty"`
	Secrets        map[string]string `json:"secrets,omitempty"`
	ResumeEnabled  *bool             `json:"resume_enabled,omitempty"`
	RBACExpression *string           `json:"rbac_expression,omitempty"`
}

// PipelineAPIResponse is a sanitized pipeline representation for the public API.
type PipelineAPIResponse struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Content        string    `json:"content"`
	ContentType    string    `json:"content_type"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ResumeEnabled  bool      `json:"resume_enabled"`
	Paused         bool      `json:"paused"`
	RBACExpression string    `json:"rbac_expression,omitempty"`
}

func toPipelineAPIResponse(pipeline *storage.Pipeline) PipelineAPIResponse {
	if pipeline == nil {
		return PipelineAPIResponse{}
	}

	return PipelineAPIResponse{
		ID:             pipeline.ID,
		Name:           pipeline.Name,
		Content:        pipeline.Content,
		ContentType:    pipeline.ContentType,
		CreatedAt:      pipeline.CreatedAt,
		UpdatedAt:      pipeline.UpdatedAt,
		ResumeEnabled:  pipeline.ResumeEnabled,
		Paused:         pipeline.Paused,
		RBACExpression: pipeline.RBACExpression,
	}
}

// APIPipelinesController handles JSON API endpoints for pipelines. Handler
// methods are grouped by concern across sibling files: upsert + schedule
// sync in api_pipelines_upsert.go, trigger paths in api_pipelines_trigger.go,
// synchronous run + workdir handling in api_pipelines_run.go, and
// pause/unpause/seed in api_pipelines_state.go.
type APIPipelinesController struct {
	BaseController
	logger          *slog.Logger
	allowedDrivers  []string
	allowedFeatures []Feature
	secretsMgr      secrets.Manager
	// maxWorkdirBytes caps the decompressed "workdir" upload on Run.
	// 0 falls back to the default inside parseRunInput.
	maxWorkdirBytes int64
}

// formatActor extracts the authenticated actor identity from the request context for audit logging.
func formatActor(ctx *echo.Context) string {
	actor, ok := auth.RequestActorFromContext(ctx.Request().Context())
	if !ok {
		return "unknown"
	}

	if actor.Provider != "" {
		return actor.Provider + ":" + actor.User
	}

	return actor.User
}

const pipelineDriverSecretKey = "driver"

// errHandled is returned by helper functions that have already written an HTTP
// response. Callers should return nil to prevent Echo from double-writing.
var errHandled = errors.New("response already sent")

// respondJSON writes a JSON error response and returns errHandled so the caller
// knows to stop processing.
func respondJSON(ctx *echo.Context, code int, body any) error {
	_ = ctx.JSON(code, body)

	return errHandled
}

// checkPipelineRBAC evaluates a pipeline's RBAC expression against the current user.
// Returns nil if access is allowed, or an error response if denied.
func checkPipelineRBAC(ctx *echo.Context, pipeline *storage.Pipeline) error {
	if pipeline.RBACExpression == "" {
		return nil
	}

	user := auth.GetUser(ctx)
	if user == nil {
		// No OAuth user in context — basic auth cannot satisfy RBAC expressions.
		return respondJSON(ctx, http.StatusForbidden, map[string]string{
			"error": "pipeline requires OAuth authentication for RBAC evaluation",
		})
	}

	allowed, err := auth.EvaluateAccess(pipeline.RBACExpression, *user)
	if err != nil || !allowed {
		return respondJSON(ctx, http.StatusForbidden, map[string]string{
			"error": "access denied to this pipeline",
		})
	}

	return nil
}

// Index handles GET /api/pipelines - List all pipelines.
func (c *APIPipelinesController) Index(ctx *echo.Context) error {
	page, perPage := parsePagination(ctx)

	result, err := c.store.SearchPipelines(ctx.Request().Context(), "", page, perPage)
	if err != nil {
		jsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if jsonErr != nil {
			return fmt.Errorf("list pipelines error response: %w", jsonErr)
		}

		return nil
	}

	if result == nil {
		result = &storage.PaginationResult[storage.Pipeline]{
			Items:      []storage.Pipeline{},
			Page:       page,
			PerPage:    perPage,
			TotalItems: 0,
			TotalPages: 0,
			HasNext:    false,
		}
	}

	items := make([]PipelineAPIResponse, 0, len(result.Items))
	for i := range result.Items {
		item := result.Items[i]

		// Filter by pipeline-level RBAC.
		if item.RBACExpression != "" {
			user := auth.GetUser(ctx)
			if user == nil {
				// No OAuth user — RBAC cannot be evaluated; hide the pipeline.
				continue
			}

			allowed, err := auth.EvaluateAccess(item.RBACExpression, *user)
			if err != nil || !allowed {
				continue
			}
		}

		items = append(items, toPipelineAPIResponse(&item))
	}

	err = ctx.JSON(http.StatusOK, storage.PaginationResult[PipelineAPIResponse]{
		Items:      items,
		Page:       result.Page,
		PerPage:    result.PerPage,
		TotalItems: result.TotalItems,
		TotalPages: result.TotalPages,
		HasNext:    result.HasNext,
	})
	if err != nil {
		return fmt.Errorf("list pipelines response: %w", err)
	}

	return nil
}

// Show handles GET /api/pipelines/:id - Get a specific pipeline.
func (c *APIPipelinesController) Show(ctx *echo.Context) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			jsonErr2 := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
			if jsonErr2 != nil {
				return fmt.Errorf("show pipeline not found response: %w", jsonErr2)
			}

			return nil
		}

		jsonErr3 := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if jsonErr3 != nil {
			return fmt.Errorf("show pipeline error response: %w", jsonErr3)
		}

		return nil
	}

	rbacErr := checkPipelineRBAC(ctx, pipeline)
	if rbacErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	err = ctx.JSON(http.StatusOK, toPipelineAPIResponse(pipeline))
	if err != nil {
		return fmt.Errorf("show pipeline response: %w", err)
	}

	return nil
}

// Destroy handles DELETE /api/pipelines/:id - Delete a pipeline.
func (c *APIPipelinesController) Destroy(ctx *echo.Context) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			jsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
			if jsonErr != nil {
				return fmt.Errorf("destroy not found response: %w", jsonErr)
			}

			return nil
		}

		jsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if jsonErr != nil {
			return fmt.Errorf("destroy get error response: %w", jsonErr)
		}

		return nil
	}

	rbacErr := checkPipelineRBAC(ctx, pipeline)
	if rbacErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	err = c.store.DeletePipeline(ctx.Request().Context(), id)
	if err != nil {
		delJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if delJsonErr != nil {
			return fmt.Errorf("destroy delete error response: %w", delJsonErr)
		}

		return nil
	}

	// Cascade delete pipeline-scoped secrets
	if c.secretsMgr != nil {
		_ = c.secretsMgr.DeleteByScope(ctx.Request().Context(), secrets.PipelineScope(id))
	}

	c.logger.Info("pipeline.delete",
		slog.String("pipeline", pipeline.Name),
		slog.String("pipeline_id", pipeline.ID),
		slog.String("actor", formatActor(ctx)),
	)

	noContentErr := ctx.NoContent(http.StatusNoContent)
	if noContentErr != nil {
		return fmt.Errorf("delete pipeline response: %w", noContentErr)
	}

	return nil
}

// RegisterRoutes registers all pipeline API routes on the given group.
func (c *APIPipelinesController) RegisterRoutes(api *echo.Group) {
	api.GET("/pipelines", c.Index)
	api.GET("/pipelines/:id", c.Show)
	api.PUT("/pipelines/:name", c.Upsert)
	api.DELETE("/pipelines/:id", c.Destroy)
	api.POST("/pipelines/:id/trigger", c.Trigger)
	api.POST("/pipelines/:id/pause", c.Pause)
	api.POST("/pipelines/:id/unpause", c.Unpause)
	api.POST("/pipelines/:name/run", c.Run)
	api.POST("/pipelines/:id/jobs/:name/seed-passed", c.SeedJobPassed)
	api.GET("/pipelines/:id/schedules", c.ListSchedules)
}

// ListSchedules handles GET /api/pipelines/:id/schedules - List schedules for a pipeline.
func (c *APIPipelinesController) ListSchedules(ctx *echo.Context) error {
	id := ctx.Param("id")

	_, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			lsNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
			if lsNFJsonErr != nil {
				return fmt.Errorf("list schedules not found response: %w", lsNFJsonErr)
			}

			return nil
		}

		lsGetJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if lsGetJsonErr != nil {
			return fmt.Errorf("list schedules get error response: %w", lsGetJsonErr)
		}

		return nil
	}

	schedules, err := c.store.GetSchedulesByPipeline(ctx.Request().Context(), id)
	if err != nil {
		lsErrJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if lsErrJsonErr != nil {
			return fmt.Errorf("list schedules error response: %w", lsErrJsonErr)
		}

		return nil
	}

	lsOkJsonErr := ctx.JSON(http.StatusOK, schedules)
	if lsOkJsonErr != nil {
		return fmt.Errorf("list schedules response: %w", lsOkJsonErr)
	}

	return nil
}
