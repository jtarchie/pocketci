package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/jsapi"
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
		RBACExpression: pipeline.RBACExpression,
	}
}

// APIPipelinesController handles JSON API endpoints for pipelines.
type APIPipelinesController struct {
	BaseController
	allowedDrivers  []string
	allowedFeatures []Feature
	secretsMgr      secrets.Manager
}

const pipelineDriverSecretKey = "driver"

// checkPipelineRBAC evaluates a pipeline's RBAC expression against the current user.
// Returns nil if access is allowed, or an error response if denied.
func checkPipelineRBAC(ctx *echo.Context, pipeline *storage.Pipeline) error {
	if pipeline.RBACExpression == "" {
		return nil
	}

	user := auth.GetUser(ctx)
	if user == nil {
		// No user in context means no OAuth configured — allow access.
		return nil
	}

	allowed, err := auth.EvaluateAccess(pipeline.RBACExpression, *user)
	if err != nil || !allowed {
		return ctx.JSON(http.StatusForbidden, map[string]string{
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

		// Filter by pipeline-level RBAC if a user is present.
		if item.RBACExpression != "" {
			user := auth.GetUser(ctx)
			if user != nil {
				allowed, err := auth.EvaluateAccess(item.RBACExpression, *user)
				if err != nil || !allowed {
					continue
				}
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
		return err
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
		return fmt.Errorf("secrets feature is not enabled")
	}

	if c.secretsMgr == nil {
		return fmt.Errorf("secrets backend is not configured on the server")
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

// Upsert handles PUT /api/pipelines/:name - Create or update a pipeline by name.
func (c *APIPipelinesController) Upsert(ctx *echo.Context) error {
	name := ctx.Param("name")

	if name == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "name is required",
		})
	}

	var req PipelineRequest
	if err := ctx.Bind(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if req.Content == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "content is required",
		})
	}

	if req.Driver == "" {
		req.Driver = c.execService.DefaultDriver
	}

	if err := orchestra.IsDriverAllowed(req.Driver, c.allowedDrivers); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("driver not allowed: %v", err),
		})
	}

	if req.WebhookSecret != nil && *req.WebhookSecret != "" && !IsFeatureEnabled(FeatureWebhooks, c.allowedFeatures) {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "webhooks feature is not enabled",
		})
	}

	if req.WebhookSecret != nil && *req.WebhookSecret != "" && c.secretsMgr == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "secrets backend is not configured on the server",
		})
	}

	if err := c.validateSecrets(ctx.Request().Context(), name, req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
	}

	if !IsFeatureEnabled(FeatureSecrets, c.allowedFeatures) {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "secrets feature is not enabled",
		})
	}

	if c.secretsMgr == nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "secrets backend is not configured on the server",
		})
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

	if req.ResumeEnabled != nil && *req.ResumeEnabled {
		if !IsFeatureEnabled(FeatureResume, c.allowedFeatures) {
			return ctx.JSON(http.StatusBadRequest, map[string]string{
				"error": "resume feature is not enabled",
			})
		}
	}

	if req.ResumeEnabled != nil {
		if err := c.store.UpdatePipelineResumeEnabled(ctx.Request().Context(), pipeline.ID, *req.ResumeEnabled); err != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to update resume_enabled: %v", err),
			})
		}

		pipeline.ResumeEnabled = *req.ResumeEnabled
	}

	// Handle RBAC expression update
	if req.RBACExpression != nil {
		if *req.RBACExpression != "" {
			if err := auth.ValidateExpression(*req.RBACExpression); err != nil {
				return ctx.JSON(http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("invalid RBAC expression: %v", err),
				})
			}
		}

		if err := c.store.UpdatePipelineRBACExpression(ctx.Request().Context(), pipeline.ID, *req.RBACExpression); err != nil {
			return ctx.JSON(http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to update rbac_expression: %v", err),
			})
		}

		pipeline.RBACExpression = *req.RBACExpression
	}

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
		return err
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

// Trigger handles POST /api/pipelines/:id/trigger - Trigger pipeline execution.
// Accepts an optional JSON body to trigger with args or a simulated webhook payload.
func (c *APIPipelinesController) Trigger(ctx *echo.Context) error {
	id := ctx.Param("id")

	if !c.execService.CanExecute() {
		if isHtmxRequest(ctx) {
			return ctx.String(http.StatusTooManyRequests, "Max concurrent executions reached")
		}
		return ctx.JSON(http.StatusTooManyRequests, map[string]any{
			"error":         "max concurrent executions reached",
			"in_flight":     c.execService.CurrentInFlight(),
			"max_in_flight": c.execService.MaxInFlight(),
		})
	}

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
		return err
	}

	// Parse optional JSON body for trigger mode.
	var req triggerRequest
	if ctx.Request().ContentLength > 0 {
		_ = json.NewDecoder(ctx.Request().Body).Decode(&req)
	}

	var run *storage.PipelineRun

	switch req.Mode {
	case "args":
		run, err = c.execService.TriggerPipeline(ctx.Request().Context(), pipeline, req.Args)

	case "webhook":
		if !IsFeatureEnabled(FeatureWebhooks, c.allowedFeatures) {
			return ctx.JSON(http.StatusForbidden, map[string]string{
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
		run, err = c.execService.TriggerWebhookPipeline(ctx.Request().Context(), pipeline, webhookData, responseChan)

	default:
		// mode="" or "manual" — current behavior
		run, err = c.execService.TriggerPipeline(ctx.Request().Context(), pipeline, nil)
	}

	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to trigger pipeline: %v", err),
		})
	}

	if isHtmxRequest(ctx) {
		ctx.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":{"message":"%s triggered successfully","type":"success"}}`, pipeline.Name))

		return ctx.NoContent(http.StatusOK)
	}

	return ctx.JSON(http.StatusAccepted, map[string]any{
		"run_id":      run.ID,
		"pipeline_id": pipeline.ID,
		"status":      run.Status,
		"message":     "pipeline execution started",
	})
}

// Run handles POST /api/pipelines/:name/run - Run a stored pipeline by name (synchronous SSE stream).
func (c *APIPipelinesController) Run(ctx *echo.Context) error {
	name := ctx.Param("name")

	var args []string
	var workdirTar io.Reader

	// Try multipart streaming first (preferred: allows large workdir tars without buffering).
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
				// workdir part found, stop iterating to preserve the reader.
				break
			}
		}
	} else {
		// Fall back to JSON body (no workdir support in this path).
		var req struct {
			Args []string `json:"args"`
		}
		_ = json.NewDecoder(ctx.Request().Body).Decode(&req)
		args = req.Args
	}

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
		return err
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

// RegisterRoutes registers all pipeline API routes on the given group.
func (c *APIPipelinesController) RegisterRoutes(api *echo.Group) {
	api.GET("/pipelines", c.Index)
	api.GET("/pipelines/:id", c.Show)
	api.PUT("/pipelines/:name", c.Upsert)
	api.DELETE("/pipelines/:id", c.Destroy)
	api.POST("/pipelines/:id/trigger", c.Trigger)
	api.POST("/pipelines/:name/run", c.Run)
}
