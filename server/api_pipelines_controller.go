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

	err := c.secretsMgr.Set(ctx, scope, pipelineDriverSecretKey, req.Driver)
	if err != nil {
		return fmt.Errorf("failed to store driver: %w", err)
	}

	// Store driver config as a single JSON secret
	if len(req.DriverConfig) > 0 {
		err := c.secretsMgr.Set(ctx, scope, "driver_config", string(req.DriverConfig))
		if err != nil {
			return fmt.Errorf("failed to store driver config: %w", err)
		}
	}

	if req.WebhookSecret != nil {
		if *req.WebhookSecret == "" {
			err := c.secretsMgr.Delete(ctx, scope, "webhook_secret")
			if err != nil && !errors.Is(err, secrets.ErrNotFound) {
				return fmt.Errorf("failed to delete webhook secret: %w", err)
			}
		} else {
			err := c.secretsMgr.Set(ctx, scope, "webhook_secret", *req.WebhookSecret)
			if err != nil {
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
			err := c.secretsMgr.Set(ctx, scope, key, req.Secrets[key])
			if err != nil {
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

	err := orchestra.IsDriverAllowed(req.Driver, c.allowedDrivers)
	if err != nil {
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

	err = c.validateSecrets(ctx.Request().Context(), name, *req)
	if err != nil {
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
		err := c.store.UpdatePipeline(ctx.Request().Context(), pipeline.ID, storage.PipelineUpdate{ResumeEnabled: req.ResumeEnabled})
		if err != nil {
			return respondJSON(ctx, http.StatusInternalServerError, map[string]string{
				"error": "internal server error",
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

			err := auth.ValidateExpression(*req.RBACExpression)
			if err != nil {
				return respondJSON(ctx, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("invalid RBAC expression: %v", err),
				})
			}
		}

		oldExpression := pipeline.RBACExpression

		err := c.store.UpdatePipeline(ctx.Request().Context(), pipeline.ID, storage.PipelineUpdate{RBACExpression: req.RBACExpression})
		if err != nil {
			return respondJSON(ctx, http.StatusInternalServerError, map[string]string{
				"error": "internal server error",
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

// upsertCheckExistingRBAC fetches the pipeline by name and enforces RBAC if it
// already exists. Returns (nil, nil) when the pipeline does not exist yet (new
// pipeline). When a non-nil error is returned the HTTP response has already
// been written.
func (c *APIPipelinesController) upsertCheckExistingRBAC(ctx *echo.Context, name string) (*storage.Pipeline, error) {
	existing, err := c.store.GetPipelineByName(ctx.Request().Context(), name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil //nolint:nilnil // nil pipeline + nil error = new pipeline, not an error condition
		}

		jsonErr4 := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if jsonErr4 != nil {
			return nil, fmt.Errorf("upsert check existing error response: %w", jsonErr4)
		}

		return nil, errHandled
	}

	rbacErr2 := checkPipelineRBAC(ctx, existing)
	if rbacErr2 != nil {
		return nil, rbacErr2
	}

	return existing, nil
}

// Upsert handles PUT /api/pipelines/:name - Create or update a pipeline by name.
func (c *APIPipelinesController) Upsert(ctx *echo.Context) error {
	name := ctx.Param("name")

	var req PipelineRequest
	bindErr := ctx.Bind(&req)
	if bindErr != nil {
		jsonErr5 := ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
		if jsonErr5 != nil {
			return fmt.Errorf("upsert bad request response: %w", jsonErr5)
		}

		return nil
	}

	validateErr := c.validateUpsertRequest(ctx, name, &req)
	if validateErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	// For updates, check RBAC on the existing pipeline before allowing the save.
	existing, err := c.upsertCheckExistingRBAC(ctx, name)
	if err != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	pipeline, err := c.store.SavePipeline(ctx.Request().Context(), name, req.Content, req.Driver, req.ContentType)
	if err != nil {
		jsonErr6 := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if jsonErr6 != nil {
			return fmt.Errorf("upsert save error response: %w", jsonErr6)
		}

		return nil
	}

	persistErr := c.persistSecrets(ctx.Request().Context(), pipeline, req)
	if persistErr != nil {
		jsonErr7 := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": persistErr.Error(),
		})
		if jsonErr7 != nil {
			return fmt.Errorf("upsert persist secrets error response: %w", jsonErr7)
		}

		return nil
	}

	postSaveErr := c.upsertPostSave(ctx, pipeline, req)
	if postSaveErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	syncErr := syncSchedules(ctx.Request().Context(), c.store, pipeline)
	if syncErr != nil {
		c.logger.Error("pipeline.sync_schedules.failed",
			slog.String("pipeline_id", pipeline.ID),
			slog.String("error", syncErr.Error()),
		)
	}

	c.logger.Info("pipeline.upsert",
		slog.String("pipeline", pipeline.Name),
		slog.String("pipeline_id", pipeline.ID),
		slog.String("actor", formatActor(ctx)),
		slog.Bool("created", existing == nil),
	)

	err = ctx.JSON(http.StatusOK, toPipelineAPIResponse(pipeline))
	if err != nil {
		return fmt.Errorf("upsert response: %w", err)
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

// triggerNotFound writes an appropriate 404 response for HTMX or JSON clients.
func (c *APIPipelinesController) triggerNotFound(ctx *echo.Context) error {
	if isHtmxRequest(ctx) {
		strErr := ctx.String(http.StatusNotFound, "Pipeline not found")
		if strErr != nil {
			return fmt.Errorf("trigger pipeline not found response: %w", strErr)
		}

		return nil
	}

	jsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
		"error": "pipeline not found",
	})
	if jsonErr != nil {
		return fmt.Errorf("trigger not found response: %w", jsonErr)
	}

	return nil
}

// triggerPipelinePaused writes an appropriate 409 response for HTMX or JSON clients.
func (c *APIPipelinesController) triggerPipelinePaused(ctx *echo.Context) error {
	if isHtmxRequest(ctx) {
		strErr := ctx.String(http.StatusConflict, "Pipeline is paused")
		if strErr != nil {
			return fmt.Errorf("trigger paused response: %w", strErr)
		}

		return nil
	}

	jsonErr := ctx.JSON(http.StatusConflict, map[string]string{
		"error": "pipeline is paused",
	})
	if jsonErr != nil {
		return fmt.Errorf("trigger paused json response: %w", jsonErr)
	}

	return nil
}

// triggerQueueFull writes an appropriate 429 response for HTMX or JSON clients.
func (c *APIPipelinesController) triggerQueueFull(ctx *echo.Context) error {
	if isHtmxRequest(ctx) {
		strErr := ctx.String(http.StatusTooManyRequests, "Execution queue is full")
		if strErr != nil {
			return fmt.Errorf("trigger queue full response: %w", strErr)
		}

		return nil
	}

	jsonErr := ctx.JSON(http.StatusTooManyRequests, map[string]any{
		"error":          "execution queue is full",
		"in_flight":      c.execService.CurrentInFlight(),
		"max_in_flight":  c.execService.MaxInFlight(),
		"max_queue_size": c.execService.MaxQueueSize(),
	})
	if jsonErr != nil {
		return fmt.Errorf("trigger queue full json response: %w", jsonErr)
	}

	return nil
}

// Trigger handles POST /api/pipelines/:id/trigger - Trigger pipeline execution.
// Accepts an optional JSON body to trigger with args or a simulated webhook payload.
func (c *APIPipelinesController) Trigger(ctx *echo.Context) error {
	id := ctx.Param("id")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return c.triggerNotFound(ctx)
		}

		getJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if getJsonErr != nil {
			return fmt.Errorf("trigger get error response: %w", getJsonErr)
		}

		return nil
	}

	rbacErr := checkPipelineRBAC(ctx, pipeline)
	if rbacErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	if pipeline.Paused {
		return c.triggerPipelinePaused(ctx)
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
			return c.triggerQueueFull(ctx)
		}

		trigJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if trigJsonErr != nil {
			return fmt.Errorf("trigger error response: %w", trigJsonErr)
		}

		return nil
	}

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":{"message":"%s triggered successfully","type":"success"}}`, pipeline.Name))

		noContentErr := ctx.NoContent(http.StatusOK)
		if noContentErr != nil {
			return fmt.Errorf("trigger ok response: %w", noContentErr)
		}

		return nil
	}

	acceptedErr := ctx.JSON(http.StatusAccepted, map[string]any{
		"run_id":        run.ID,
		"pipeline_id":   pipeline.ID,
		"status":        run.Status,
		"trigger_type":  run.TriggerType,
		"triggered_by":  run.TriggeredBy,
		"trigger_input": run.TriggerInput,
		"message":       "pipeline execution started",
	})
	if acceptedErr != nil {
		return fmt.Errorf("trigger accepted response: %w", acceptedErr)
	}

	return nil
}

// Run handles POST /api/pipelines/:name/run - Run a stored pipeline by name (synchronous SSE stream).
func (c *APIPipelinesController) Run(ctx *echo.Context) error {
	name := ctx.Param("name")

	args, workdirTar := parseRunInput(ctx, c.maxWorkdirBytes)
	if workdirTar != nil {
		defer func() {
			err := workdirTar.Close()
			if err != nil {
				c.logger.Warn("workdir.tar.close", slog.String("error", err.Error()))
			}
		}()
	}

	w := ctx.Response()

	// Check pipeline-level RBAC before executing.
	pipeline, err := c.store.GetPipelineByName(ctx.Request().Context(), name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			nfJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
			if nfJsonErr != nil {
				return fmt.Errorf("run not found response: %w", nfJsonErr)
			}

			return nil
		}

		geJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		if geJsonErr != nil {
			return fmt.Errorf("run get error response: %w", geJsonErr)
		}

		return nil
	}

	runRbacErr := checkPipelineRBAC(ctx, pipeline)
	if runRbacErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	if pipeline.Paused {
		pausedJsonErr := ctx.JSON(http.StatusConflict, map[string]string{
			"error": "pipeline is paused",
		})
		if pausedJsonErr != nil {
			return fmt.Errorf("run paused response: %w", pausedJsonErr)
		}

		return nil
	}

	err = c.execService.RunByNameSync(ctx.Request().Context(), name, args, workdirTar, w)
	if err != nil {
		return c.runHandleSyncError(ctx, w, err)
	}

	return nil
}

// runHandleSyncError handles an error from RunByNameSync. If the response has
// not yet been committed it writes an appropriate HTTP error; otherwise it
// appends an SSE error event to the already-started stream.
func (c *APIPipelinesController) runHandleSyncError(ctx *echo.Context, w http.ResponseWriter, runErr error) error {
	echoResp, _ := echo.UnwrapResponse(ctx.Response())
	if echoResp != nil && echoResp.Committed {
		errData, _ := json.Marshal(map[string]string{"event": "error", "message": runErr.Error()})
		fmt.Fprintf(w, "data: %s\n\n", errData) //nolint:errcheck
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		return nil
	}

	if errors.Is(runErr, storage.ErrNotFound) {
		syncNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
			"error": "pipeline not found",
		})
		if syncNFJsonErr != nil {
			return fmt.Errorf("run sync not found response: %w", syncNFJsonErr)
		}

		return nil
	}

	syncErrJson := ctx.JSON(http.StatusInternalServerError, map[string]string{
		"error": runErr.Error(),
	})
	if syncErrJson != nil {
		return fmt.Errorf("run sync error response: %w", syncErrJson)
	}

	return nil
}

// DefaultMaxWorkdirBytes is the fallback cap when the server is configured
// with 0 (or nothing wired through). Rejects zstd bombs that would expand
// past the cap. Echo's BodyLimit caps the compressed body; this caps the
// decompressed stream, which a bomb can inflate by 1,000,000× or more.
// The caller (server config, CLI flag CI_MAX_WORKDIR_MB) overrides this.
const DefaultMaxWorkdirBytes int64 = 1 << 30 // 1 GiB

// ErrWorkdirTooLarge is returned when the decompressed workdir stream
// exceeds the configured cap.
var ErrWorkdirTooLarge = errors.New("workdir decompressed size exceeds cap")

// cappedReadCloser bounds the total bytes read through it. Reads past the
// cap return ErrWorkdirTooLarge so callers (tar extractors, volume copiers)
// surface a clear error rather than silently truncating.
type cappedReadCloser struct {
	inner io.ReadCloser
	cap   int64
	read  int64
}

func (c *cappedReadCloser) Read(p []byte) (int, error) {
	remaining := c.cap - c.read
	if remaining <= 0 {
		return 0, ErrWorkdirTooLarge
	}

	if int64(len(p)) > remaining {
		p = p[:remaining]
	}

	n, err := c.inner.Read(p)
	c.read += int64(n)

	if c.read >= c.cap && (err == nil || errors.Is(err, io.EOF)) {
		// Peek one extra byte to distinguish "exactly at cap" from
		// "cap reached with more data available".
		var peek [1]byte
		pn, _ := c.inner.Read(peek[:])

		if pn > 0 {
			return n, ErrWorkdirTooLarge
		}
	}

	switch {
	case err == nil, errors.Is(err, io.EOF):
		return n, err //nolint:wrapcheck // io.EOF is a sentinel and must pass through unwrapped
	default:
		return n, fmt.Errorf("cappedRead: %w", err)
	}
}

func (c *cappedReadCloser) Close() error {
	err := c.inner.Close()
	if err != nil {
		return fmt.Errorf("cappedClose: %w", err)
	}

	return nil
}

// zstdReadCloser wraps *zstd.Decoder to satisfy io.ReadCloser.
// zstd.Decoder.Close() has no return value, so we adapt it here.
type zstdReadCloser struct{ *zstd.Decoder }

func (z zstdReadCloser) Close() error { z.Decoder.Close(); return nil }

// parseRunInput extracts args and an optional workdir tar from the request,
// trying multipart streaming first then falling back to JSON body.
//
// maxWorkdirBytes bounds the decompressed "workdir" zstd stream to reject
// zip-bomb uploads. Zero or negative values fall back to
// DefaultMaxWorkdirBytes so unwired callers still get the cap.
//
// Part ordering contract: clients must send "args" before "workdir".
// Multipart is a single stream — once we hand the zstd-wrapped "workdir"
// part to the caller, we cannot advance to further parts without
// invalidating the wrapped reader. Parts encountered after "workdir" are
// silently ignored; the CLI client already honours this ordering.
// Non-workdir parts are closed eagerly so the connection reader can advance.
func parseRunInput(ctx *echo.Context, maxWorkdirBytes int64) ([]string, io.ReadCloser) {
	if maxWorkdirBytes <= 0 {
		maxWorkdirBytes = DefaultMaxWorkdirBytes
	}

	var args []string
	var workdirTar io.ReadCloser

	mr, mrErr := ctx.Request().MultipartReader()
	if mrErr == nil {
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
				_ = part.Close()
			case "workdir":
				// The returned zstd reader wraps `part`; the caller
				// defer-closes workdirTar, which transitively finishes
				// the part. Return immediately — see ordering contract.
				zr, zErr := zstd.NewReader(part)
				if zErr != nil {
					_ = part.Close()

					continue
				}
				// Cap the decompressed stream to reject zstd bombs.
				workdirTar = &cappedReadCloser{
					inner: zstdReadCloser{zr},
					cap:   maxWorkdirBytes,
				}
			default:
				_ = part.Close()
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

// setPausedNotFound writes an appropriate 404 response for HTMX or JSON clients.
func (c *APIPipelinesController) setPausedNotFound(ctx *echo.Context) error {
	if isHtmxRequest(ctx) {
		strErr := ctx.String(http.StatusNotFound, "Pipeline not found")
		if strErr != nil {
			return fmt.Errorf("set paused not found response: %w", strErr)
		}

		return nil
	}

	jsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
		"error": "pipeline not found",
	})
	if jsonErr != nil {
		return fmt.Errorf("set paused not found json response: %w", jsonErr)
	}

	return nil
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

		saveSchedErr := store.SaveSchedule(ctx, schedule)
		if saveSchedErr != nil {
			return fmt.Errorf("save schedule for job %q: %w", s.JobName, saveSchedErr)
		}

		keepNames = append(keepNames, s.JobName)
	}

	// Remove schedules no longer declared in the YAML.
	pruneErr := store.PruneSchedulesByPipeline(ctx, pipeline.ID, keepNames)
	if pruneErr != nil {
		return fmt.Errorf("prune stale schedules: %w", pruneErr)
	}

	return nil
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
