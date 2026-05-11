package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/scheduler"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

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
