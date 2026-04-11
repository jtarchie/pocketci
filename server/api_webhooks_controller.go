package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
	"github.com/jtarchie/pocketci/webhooks"
	"github.com/labstack/echo/v5"
)

// APIWebhooksController handles webhook trigger endpoints.
type APIWebhooksController struct {
	BaseController
	allowedFeatures []Feature
	webhookTimeout  time.Duration
	logger          *slog.Logger
	secretsMgr      secrets.Manager
	providers       []webhooks.Provider
}

// resolveWebhookSecret retrieves the webhook secret for a pipeline from the secrets manager.
func (c *APIWebhooksController) resolveWebhookSecret(ctx *echo.Context, pipeline *storage.Pipeline, logger *slog.Logger) (string, error) {
	if c.secretsMgr == nil {
		return "", nil
	}

	resolvedSecret, getErr := c.secretsMgr.Get(ctx.Request().Context(), secrets.PipelineScope(pipeline.ID), "webhook_secret")
	if getErr == nil {
		return resolvedSecret, nil
	}

	if errors.Is(getErr, secrets.ErrNotFound) {
		return "", nil
	}

	logger.Error("webhook.secret_error", "error", getErr)

	return "", respondJSON(ctx, http.StatusInternalServerError, map[string]string{
		"error": fmt.Sprintf("failed to get webhook secret: %v", getErr),
	})
}

// detectWebhookEvent reads the request body, logs headers, and detects the webhook provider/event.
func (c *APIWebhooksController) detectWebhookEvent(ctx *echo.Context, webhookSecret string, logger *slog.Logger) (*webhooks.Event, error) {
	body, err := io.ReadAll(ctx.Request().Body)
	if err != nil {
		logger.Error("webhook.read_body_error", "error", err)

		return nil, respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": "failed to read request body",
		})
	}

	// Log header names only — values may contain signature material or tokens.
	headerNames := make([]string, 0, len(ctx.Request().Header))
	for k := range ctx.Request().Header {
		headerNames = append(headerNames, k)
	}
	logger.Debug("webhook.detecting", "body_bytes", len(body), "header_names", headerNames)

	event, err := webhooks.Detect(c.providers, ctx.Request(), body, webhookSecret)
	if err != nil {
		if errors.Is(err, webhooks.ErrUnauthorized) {
			// Do not log signature header values — they are cryptographic material
			// and logging them aids offline secret-recovery attempts.
			logger.Error("webhook.unauthorized",
				"has_hub_signature", ctx.Request().Header.Get("X-Hub-Signature-256") != "",
				"has_slack_signature", ctx.Request().Header.Get("X-Slack-Signature") != "",
				"has_generic_signature", ctx.Request().Header.Get("X-Webhook-Signature") != "",
			)

			return nil, respondJSON(ctx, http.StatusUnauthorized, map[string]string{
				"error": "webhook signature validation failed",
			})
		}

		logger.Error("webhook.detect_error", "error", err)

		return nil, respondJSON(ctx, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("no webhook provider matched the request: %v", err),
		})
	}

	return event, nil
}

// triggerWebhookQueueFull logs and writes an HTTP 429 response when the queue is full.
func (c *APIWebhooksController) triggerWebhookQueueFull(ctx *echo.Context, logger *slog.Logger) error {
	logger.Error("webhook.queue_full",
		"in_flight", c.execService.CurrentInFlight(),
		"max_in_flight", c.execService.MaxInFlight(),
		"max_queue_size", c.execService.MaxQueueSize(),
	)

	qfJsonErr := ctx.JSON(http.StatusTooManyRequests, map[string]any{
		"error":          "execution queue is full",
		"in_flight":      c.execService.CurrentInFlight(),
		"max_in_flight":  c.execService.MaxInFlight(),
		"max_queue_size": c.execService.MaxQueueSize(),
	})
	if qfJsonErr != nil {
		return fmt.Errorf("webhook queue full response: %w", qfJsonErr)
	}

	return nil
}

// triggerWebhookPipelineResponse waits for the pipeline to reply via responseChan
// or times out, then writes the appropriate HTTP response.
func (c *APIWebhooksController) triggerWebhookPipelineResponse(ctx *echo.Context, pipeline *storage.Pipeline, run *storage.PipelineRun, responseChan <-chan *jsapi.HTTPResponse, logger *slog.Logger) error {
	select {
	case resp := <-responseChan:
		logger.Info("webhook.pipeline_responded", "run_id", run.ID, "status", resp.Status)
		for key, value := range resp.Headers {
			ctx.Response().Header().Set(key, value)
		}

		if resp.Body != "" {
			bodyStrErr := ctx.String(resp.Status, resp.Body)
			if bodyStrErr != nil {
				return fmt.Errorf("webhook response body: %w", bodyStrErr)
			}

			return nil
		}

		noContentErr := ctx.NoContent(resp.Status)
		if noContentErr != nil {
			return fmt.Errorf("webhook no content response: %w", noContentErr)
		}

		return nil

	case <-time.After(c.webhookTimeout):
		logger.Info("webhook.timeout_accepted", "run_id", run.ID)

		acceptedJsonErr := ctx.JSON(http.StatusAccepted, map[string]any{
			"run_id":      run.ID,
			"pipeline_id": pipeline.ID,
			"status":      run.Status,
			"message":     "pipeline execution started",
		})
		if acceptedJsonErr != nil {
			return fmt.Errorf("webhook accepted response: %w", acceptedJsonErr)
		}

		return nil
	}
}

// Trigger handles ANY /api/webhooks/:id - Trigger pipeline execution via webhook.
func (c *APIWebhooksController) Trigger(ctx *echo.Context) error {
	if !IsFeatureEnabled(FeatureWebhooks, c.allowedFeatures) {
		featJsonErr := ctx.JSON(http.StatusForbidden, map[string]string{
			"error": "webhooks feature is not enabled",
		})
		if featJsonErr != nil {
			return fmt.Errorf("webhooks feature disabled response: %w", featJsonErr)
		}

		return nil
	}

	id := ctx.Param("id")
	logger := LoggerWithRequestActor(LoggerWithRequestID(c.logger, ctx.Request().Context()), ctx.Request().Context()).With("pipeline_id", id, "method", ctx.Request().Method, "remote_ip", ctx.RealIP())
	logger.Info("webhook.received")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			logger.Error("webhook.pipeline_not_found")

			wNFJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
			if wNFJsonErr != nil {
				return fmt.Errorf("webhook pipeline not found response: %w", wNFJsonErr)
			}

			return nil
		}

		logger.Error("webhook.store_error", "error", err)

		wStoreJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
		if wStoreJsonErr != nil {
			return fmt.Errorf("webhook store error response: %w", wStoreJsonErr)
		}

		return nil
	}

	if pipeline.Paused {
		logger.Info("webhook.pipeline_paused")

		wPausedJsonErr := ctx.JSON(http.StatusConflict, map[string]string{
			"error": "pipeline is paused",
		})
		if wPausedJsonErr != nil {
			return fmt.Errorf("webhook pipeline paused response: %w", wPausedJsonErr)
		}

		return nil
	}

	webhookSecret, secretErr := c.resolveWebhookSecret(ctx, pipeline, logger)
	if secretErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	logger = logger.With("pipeline_name", pipeline.Name, "has_webhook_secret", webhookSecret != "")

	event, detectErr := c.detectWebhookEvent(ctx, webhookSecret, logger)
	if detectErr != nil {
		return nil //nolint:nilerr // helper already wrote the HTTP response
	}

	logger = logger.With("provider", event.Provider, "event_type", event.EventType)
	logger.Info("webhook.detected")

	webhookData := &jsapi.WebhookData{
		Provider:  event.Provider,
		EventType: event.EventType,
		Method:    event.Method,
		URL:       event.URL,
		Headers:   event.Headers,
		Body:      event.Body,
		Query:     event.Query,
	}

	responseChan := make(chan *jsapi.HTTPResponse, 1)

	run, err := c.execService.TriggerWebhookPipeline(ctx.Request().Context(), pipeline, webhookData, responseChan)
	if err != nil {
		if errors.Is(err, ErrQueueFull) {
			return c.triggerWebhookQueueFull(ctx, logger)
		}

		logger.Error("webhook.trigger_error", "error", err)

		wTriggerJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to trigger pipeline: %v", err),
		})
		if wTriggerJsonErr != nil {
			return fmt.Errorf("webhook trigger error response: %w", wTriggerJsonErr)
		}

		return nil
	}

	logger.Info("webhook.triggered", "run_id", run.ID)

	return c.triggerWebhookPipelineResponse(ctx, pipeline, run, responseChan, logger)
}

// RegisterRoutes registers all webhook routes on the main router (no auth group).
func (c *APIWebhooksController) RegisterRoutes(router *echo.Echo) {
	router.Any("/api/webhooks/:id", c.Trigger)
}
