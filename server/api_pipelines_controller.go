package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/scheduler"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	"github.com/klauspost/compress/zstd"
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

// APIPipelinesController handles JSON API endpoints for pipelines.
type APIPipelinesController struct {
	BaseController
	logger          *slog.Logger
	allowedDrivers  []string
	allowedFeatures []Feature
	secretsMgr      secrets.Manager
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
	page := 1
	perPage := 20

	if p := ctx.QueryParam("page"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &page)
	}
	if pp := ctx.QueryParam("per_page"); pp != "" {
		_, _ = fmt.Sscanf(pp, "%d", &perPage)
	}

	result, err := c.store.SearchPipelines(ctx.Request().Context(), "", page, perPage)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to list pipelines: %v", err),
		})
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

	return ctx.JSON(http.StatusOK, storage.PaginationResult[PipelineAPIResponse]{
		Items:      items,
		Page:       result.Page,
		PerPage:    result.PerPage,
		TotalItems: result.TotalItems,
		TotalPages: result.TotalPages,
		HasNext:    result.HasNext,
	})
}

// Show handles GET /api/pipelines/:id - Get a specific pipeline.
func (c *APIPipelinesController) Show(ctx *echo.Context) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	return ctx.JSON(http.StatusOK, toPipelineAPIResponse(pipeline))
}

// validateSecrets checks feature gates, rejects system-managed keys in user
// secrets, and ensures all existing user secrets are re-submitted on update.
func (c *APIPipelinesController) validateSecrets(ctx context.Context, name string, req PipelineRequest) error {
	if len(req.Secrets) == 0 {
		return nil
	}

	if !IsFeatureEnabled(FeatureSecrets, c.allowedFeatures) {
		return errors.New("secrets feature is not enabled")
	}

	if c.secretsMgr == nil {
		return errors.New("secrets backend is not configured on the server")
	}

	for key := range req.Secrets {
		if secrets.IsSystemKey(key) {
			return fmt.Errorf("secret key %q is reserved for system use", key)
		}
	}

	existingPipeline, err := c.store.GetPipelineByName(ctx, name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}

		return fmt.Errorf("failed to get existing pipeline by name: %w", err)
	}

	scope := secrets.PipelineScope(existingPipeline.ID)

	existingKeys, err := c.secretsMgr.ListByScope(ctx, scope)
	if err != nil {
		return fmt.Errorf("failed to list existing secrets: %w", err)
	}

	for _, existingKey := range existingKeys {
		if secrets.IsSystemKey(existingKey) {
			continue
		}

		if _, ok := req.Secrets[existingKey]; !ok {
			return fmt.Errorf("missing existing secret key %q: all existing secrets must be included on update", existingKey)
		}
	}

	return nil
}

// persistSecrets stores the driver, driver config, webhook secret, and user-provided
// secrets for the given pipeline.
func (c *APIPipelinesController) persistSecrets(ctx context.Context, pipeline *storage.Pipeline, req PipelineRequest) error {
	scope := secrets.PipelineScope(pipeline.ID)

	if err := c.secretsMgr.Set(ctx, scope, pipelineDriverSecretKey, req.Driver); err != nil {
		return fmt.Errorf("failed to store driver: %w", err)
	}

	// Store driver config as a single JSON secret
	if len(req.DriverConfig) > 0 {
		if err := c.secretsMgr.Set(ctx, scope, "driver_config", string(req.DriverConfig)); err != nil {
			return fmt.Errorf("failed to store driver config: %w", err)
		}
	}

	if req.WebhookSecret != nil {
		if *req.WebhookSecret == "" {
			if err := c.secretsMgr.Delete(ctx, scope, "webhook_secret"); err != nil && !errors.Is(err, secrets.ErrNotFound) {
				return fmt.Errorf("failed to delete webhook secret: %w", err)
			}
		} else {
			if err := c.secretsMgr.Set(ctx, scope, "webhook_secret", *req.WebhookSecret); err != nil {
				return fmt.Errorf("failed to store webhook secret: %w", err)
			}
		}
	}

	if len(req.Secrets) > 0 {
		sortedKeys := make([]string, 0, len(req.Secrets))
		for k := range req.Secrets {
			sortedKeys = append(sortedKeys, k)
		}
		sort.Strings(sortedKeys)

		for _, key := range sortedKeys {
			if err := c.secretsMgr.Set(ctx, scope, key, req.Secrets[key]); err != nil {
				return fmt.Errorf("failed to store secret %q: %w", key, err)
			}
		}
	}

	return nil
}

// validateUpsertRequest validates and normalizes a PipelineRequest for Upsert.
func (c *APIPipelinesController) validateUpsertRequest(ctx *echo.Context, name string, req *PipelineRequest) error {
	if name == "" {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": "name is required",
		})
	}

	if req.Content == "" {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": "content is required",
		})
	}

	if req.Driver == "" {
		req.Driver = c.execService.DefaultDriver
	}

	if err := orchestra.IsDriverAllowed(req.Driver, c.allowedDrivers); err != nil {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("driver not allowed: %v", err),
		})
	}

	if req.WebhookSecret != nil && *req.WebhookSecret != "" && !IsFeatureEnabled(FeatureWebhooks, c.allowedFeatures) {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": "webhooks feature is not enabled",
		})
	}

	if req.WebhookSecret != nil && *req.WebhookSecret != "" && c.secretsMgr == nil {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": "secrets backend is not configured on the server",
		})
	}

	if err := c.validateSecrets(ctx.Request().Context(), name, *req); err != nil {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
	}

	if !IsFeatureEnabled(FeatureSecrets, c.allowedFeatures) {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": "secrets feature is not enabled",
		})
	}

	if c.secretsMgr == nil {
		return respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": "secrets backend is not configured on the server",
		})
	}

	return nil
}

// upsertPostSave handles resume and RBAC updates after a pipeline is saved.
func (c *APIPipelinesController) upsertPostSave(ctx *echo.Context, pipeline *storage.Pipeline, req PipelineRequest) error {
	if req.ResumeEnabled != nil && *req.ResumeEnabled {
		if !IsFeatureEnabled(FeatureResume, c.allowedFeatures) {
			return respondJSON(ctx, http.StatusBadRequest, map[string]string{
				"error": "resume feature is not enabled",
			})
		}
	}

	if req.ResumeEnabled != nil {
		if err := c.store.UpdatePipeline(ctx.Request().Context(), pipeline.ID, storage.PipelineUpdate{ResumeEnabled: req.ResumeEnabled}); err != nil {
			return respondJSON(ctx, http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to update resume_enabled: %v", err),
			})
		}

		pipeline.ResumeEnabled = *req.ResumeEnabled
	}

	if req.RBACExpression != nil {
		if *req.RBACExpression != "" {
			if auth.GetUser(ctx) == nil {
				return respondJSON(ctx, http.StatusBadRequest, map[string]string{
					"error": "pipeline RBAC expressions require OAuth authentication; basic auth cannot evaluate RBAC",
				})
			}

			if err := auth.ValidateExpression(*req.RBACExpression); err != nil {
				return respondJSON(ctx, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("invalid RBAC expression: %v", err),
				})
			}
		}

		oldExpression := pipeline.RBACExpression

		if err := c.store.UpdatePipeline(ctx.Request().Context(), pipeline.ID, storage.PipelineUpdate{RBACExpression: req.RBACExpression}); err != nil {
			return respondJSON(ctx, http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to update rbac_expression: %v", err),
			})
		}

		pipeline.RBACExpression = *req.RBACExpression

		c.logger.Info("pipeline.rbac.update",
			slog.String("pipeline", pipeline.Name),
			slog.String("pipeline_id", pipeline.ID),
			slog.String("actor", formatActor(ctx)),
			slog.String("old_expression", oldExpression),
			slog.String("new_expression", *req.RBACExpression),
		)
	}

	return nil
}

// Upsert handles PUT /api/pipelines/:name - Create or update a pipeline by name.
func (c *APIPipelinesController) Upsert(ctx *echo.Context) error {
	name := ctx.Param("name")

	var req PipelineRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if err := c.validateUpsertRequest(ctx, name, &req); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	// For updates, check RBAC on the existing pipeline before allowing the save.
	existing, err := c.store.GetPipelineByName(ctx.Request().Context(), name)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to check existing pipeline: %v", err),
		})
	}

	if existing != nil {
		if err := checkPipelineRBAC(ctx, existing); err != nil {
			return nil //nolint:nilerr // helper already wrote the HTTP response
		}
	}

	pipeline, err := c.store.SavePipeline(ctx.Request().Context(), name, req.Content, req.Driver, req.ContentType)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to save pipeline: %v", err),
		})
	}

	if err := c.persistSecrets(ctx.Request().Context(), pipeline, req); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
	}

	if err := c.upsertPostSave(ctx, pipeline, req); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	if err := syncSchedules(ctx.Request().Context(), c.store, pipeline); err != nil {
		c.logger.Error("pipeline.sync_schedules.failed",
			slog.String("pipeline_id", pipeline.ID),
			slog.String("error", err.Error()),
		)
	}

	c.logger.Info("pipeline.upsert",
		slog.String("pipeline", pipeline.Name),
		slog.String("pipeline_id", pipeline.ID),
		slog.String("actor", formatActor(ctx)),
		slog.Bool("created", existing == nil),
	)

	return ctx.JSON(http.StatusOK, toPipelineAPIResponse(pipeline))
}

// Destroy handles DELETE /api/pipelines/:id - Delete a pipeline.
func (c *APIPipelinesController) Destroy(ctx *echo.Context) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	err = c.store.DeletePipeline(ctx.Request().Context(), id)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to delete pipeline: %v", err),
		})
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

	return ctx.NoContent(http.StatusNoContent)
}

// triggerRequest is the optional JSON body for POST /api/pipelines/:id/trigger.
type triggerRequest struct {
	Mode    string          `json:"mode"`    // "" or "manual" (default), "args", "webhook"
	Args    []string        `json:"args"`    // for mode="args"
	Webhook *webhookSimData `json:"webhook"` // for mode="webhook"
}

// webhookSimData holds simulated webhook payload fields from the UI.
type webhookSimData struct {
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
	Method  string            `json:"method"`
}

// triggerByMode dispatches the trigger request to the appropriate execution
// path based on its mode field. Returns the created run or an error response.
func (c *APIPipelinesController) triggerByMode(ctx *echo.Context, pipeline *storage.Pipeline, req triggerRequest) (*storage.PipelineRun, error) {
	switch req.Mode {
	case "args":
		return c.execService.TriggerPipeline(ctx.Request().Context(), pipeline, req.Args)

	case "webhook":
		if !IsFeatureEnabled(FeatureWebhooks, c.allowedFeatures) {
			return nil, respondJSON(ctx, http.StatusForbidden, map[string]string{
				"error": "webhooks feature is not enabled",
			})
		}

		method := "POST"
		if req.Webhook != nil && req.Webhook.Method != "" {
			method = req.Webhook.Method
		}

		webhookData := &jsapi.WebhookData{
			Provider: "manual",
			Method:   method,
		}
		if req.Webhook != nil {
			webhookData.Body = req.Webhook.Body
			webhookData.Headers = req.Webhook.Headers
		}

		responseChan := make(chan *jsapi.HTTPResponse, 1)

		return c.execService.TriggerWebhookPipeline(ctx.Request().Context(), pipeline, webhookData, responseChan)

	default:
		return c.execService.TriggerPipeline(ctx.Request().Context(), pipeline, nil)
	}
}

// Trigger handles POST /api/pipelines/:id/trigger - Trigger pipeline execution.
// Accepts an optional JSON body to trigger with args or a simulated webhook payload.
func (c *APIPipelinesController) Trigger(ctx *echo.Context) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			if isHtmxRequest(ctx) {
				return ctx.String(http.StatusNotFound, "Pipeline not found")
			}
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	if pipeline.Paused {
		if isHtmxRequest(ctx) {
			return ctx.String(http.StatusConflict, "Pipeline is paused")
		}

		return ctx.JSON(http.StatusConflict, map[string]string{
			"error": "pipeline is paused",
		})
	}

	// Parse optional JSON body for trigger mode.
	var req triggerRequest
	if ctx.Request().ContentLength > 0 {
		_ = json.NewDecoder(ctx.Request().Body).Decode(&req)
	}

	run, err := c.triggerByMode(ctx, pipeline, req)
	if err != nil {
		if errors.Is(err, errHandled) {
			return nil
		}

		if errors.Is(err, ErrQueueFull) {
			if isHtmxRequest(ctx) {
				return ctx.String(http.StatusTooManyRequests, "Execution queue is full")
			}

			return ctx.JSON(http.StatusTooManyRequests, map[string]any{
				"error":          "execution queue is full",
				"in_flight":      c.execService.CurrentInFlight(),
				"max_in_flight":  c.execService.MaxInFlight(),
				"max_queue_size": c.execService.MaxQueueSize(),
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to trigger pipeline: %v", err),
		})
	}

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":{"message":"%s triggered successfully","type":"success"}}`, pipeline.Name))

		return ctx.NoContent(http.StatusOK)
	}

	return ctx.JSON(http.StatusAccepted, map[string]any{
		"run_id":        run.ID,
		"pipeline_id":   pipeline.ID,
		"status":        run.Status,
		"trigger_type":  run.TriggerType,
		"triggered_by":  run.TriggeredBy,
		"trigger_input": run.TriggerInput,
		"message":       "pipeline execution started",
	})
}

// Run handles POST /api/pipelines/:name/run - Run a stored pipeline by name (synchronous SSE stream).
func (c *APIPipelinesController) Run(ctx *echo.Context) error {
	name := ctx.Param("name")

	args, workdirTar := parseRunInput(ctx)

	w := ctx.Response()

	// Check pipeline-level RBAC before executing.
	pipeline, err := c.store.GetPipelineByName(ctx.Request().Context(), name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	if pipeline.Paused {
		return ctx.JSON(http.StatusConflict, map[string]string{
			"error": "pipeline is paused",
		})
	}

	err = c.execService.RunByNameSync(ctx.Request().Context(), name, args, workdirTar, w)
	if err != nil {
		echoResp, _ := echo.UnwrapResponse(ctx.Response())
		if echoResp == nil || !echoResp.Committed {
			if errors.Is(err, storage.ErrNotFound) {
				return ctx.JSON(http.StatusNotFound, map[string]string{
					"error": "pipeline not found",
				})
			}
			return ctx.JSON(http.StatusInternalServerError, map[string]string{
				"error": err.Error(),
			})
		}

		errData, _ := json.Marshal(map[string]string{"event": "error", "message": err.Error()})
		fmt.Fprintf(w, "data: %s\n\n", errData) //nolint:errcheck
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	return nil
}

// parseRunInput extracts args and an optional workdir tar from the request,
// trying multipart streaming first then falling back to JSON body.
func parseRunInput(ctx *echo.Context) ([]string, io.Reader) {
	var args []string
	var workdirTar io.Reader

	if mr, err := ctx.Request().MultipartReader(); err == nil {
		for {
			part, partErr := mr.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				break
			}

			switch part.FormName() {
			case "args":
				data, _ := io.ReadAll(part)
				_ = json.Unmarshal(data, &args)
			case "workdir":
				zr, zErr := zstd.NewReader(part)
				if zErr != nil {
					break
				}
				defer zr.Close()
				workdirTar = zr
			}

			if workdirTar != nil {
				break
			}
		}
	} else {
		var req struct {
			Args []string `json:"args"`
		}
		_ = json.NewDecoder(ctx.Request().Body).Decode(&req)
		args = req.Args
	}

	return args, workdirTar
}

// RegisterRoutes registers all pipeline API routes on the given group.
// Pause handles POST /api/pipelines/:id/pause - Pause a pipeline.
func (c *APIPipelinesController) Pause(ctx *echo.Context) error {
	return c.setPaused(ctx, true)
}

// Unpause handles POST /api/pipelines/:id/unpause - Unpause a pipeline.
func (c *APIPipelinesController) Unpause(ctx *echo.Context) error {
	return c.setPaused(ctx, false)
}

func (c *APIPipelinesController) setPaused(ctx *echo.Context, paused bool) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			if isHtmxRequest(ctx) {
				return ctx.String(http.StatusNotFound, "Pipeline not found")
			}

			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	if err := c.store.UpdatePipeline(ctx.Request().Context(), id, storage.PipelineUpdate{Paused: &paused}); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to update pipeline: %v", err),
		})
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

		return ctx.NoContent(http.StatusOK)
	}

	return ctx.JSON(http.StatusOK, map[string]any{
		"id":      pipeline.ID,
		"paused":  paused,
		"message": "pipeline " + action,
	})
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
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	if err := checkPipelineRBAC(ctx, pipeline); err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	// Create a synthetic run to hold the seeded record
	run, err := c.store.SaveRun(
		ctx.Request().Context(),
		pipeline.ID,
		"seed",
		formatActor(ctx),
		storage.TriggerInput{},
	)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to create seed run: %v", err),
		})
	}

	// Write the success task record at the standard path
	taskPath := "/pipeline/" + run.ID + "/jobs/" + jobName
	if err := c.store.Set(ctx.Request().Context(), taskPath, map[string]any{
		"status": "success",
		"seeded": true,
	}); err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to seed job status: %v", err),
		})
	}

	// Immediately mark the run as completed
	if err := c.store.UpdateRunStatus(ctx.Request().Context(), run.ID, storage.RunStatusSuccess, ""); err != nil {
		c.logger.Error("seed.run.update.failed", "error", err)
	}

	return ctx.JSON(http.StatusOK, map[string]any{
		"pipeline_id": pipeline.ID,
		"job":         jobName,
		"run_id":      run.ID,
		"message":     "job passed status seeded successfully",
	})
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

// syncSchedules extracts schedule declarations from YAML pipeline content
// and syncs them to the schedules table. It upserts current schedules
// (preserving user-managed fields like enabled) and prunes stale ones.
func syncSchedules(ctx context.Context, store storage.Driver, pipeline *storage.Pipeline) error {
	if pipeline.ContentType != storage.ContentTypeYAML {
		// Only YAML pipelines support inline schedule declarations.
		// For JS/TS, schedules are managed via CLI/API (future).
		return nil
	}

	extracted, err := backwards.ExtractSchedules(pipeline.Content)
	if err != nil {
		return fmt.Errorf("extract schedules: %w", err)
	}

	now := time.Now().UTC()

	keepNames := make([]string, 0, len(extracted))

	for _, s := range extracted {
		nextRunAt, err := scheduler.ComputeNextRun(s.ScheduleType, s.ScheduleExpr, now)
		if err != nil {
			return fmt.Errorf("compute next run for job %q: %w", s.JobName, err)
		}

		schedule := &storage.Schedule{
			ID:           support.UniqueID(),
			PipelineID:   pipeline.ID,
			Name:         s.JobName,
			ScheduleType: s.ScheduleType,
			ScheduleExpr: s.ScheduleExpr,
			JobName:      s.JobName,
			Enabled:      true,
			NextRunAt:    &nextRunAt,
		}

		if err := store.SaveSchedule(ctx, schedule); err != nil {
			return fmt.Errorf("save schedule for job %q: %w", s.JobName, err)
		}

		keepNames = append(keepNames, s.JobName)
	}

	// Remove schedules no longer declared in the YAML.
	if err := store.PruneSchedulesByPipeline(ctx, pipeline.ID, keepNames); err != nil {
		return fmt.Errorf("prune stale schedules: %w", err)
	}

	return nil
}

// ListSchedules handles GET /api/pipelines/:id/schedules - List schedules for a pipeline.
func (c *APIPipelinesController) ListSchedules(ctx *echo.Context) error {
	id := ctx.Param("id")

	_, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	schedules, err := c.store.GetSchedulesByPipeline(ctx.Request().Context(), id)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to list schedules: %v", err),
		})
	}

	return ctx.JSON(http.StatusOK, schedules)
}
