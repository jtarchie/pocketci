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
}

// Trigger handles ANY /api/webhooks/:id - Trigger pipeline execution via webhook.
func (c *APIWebhooksController) Trigger(ctx *echo.Context) error {
	if !IsFeatureEnabled(FeatureWebhooks, c.allowedFeatures) {
		return ctx.JSON(http.StatusForbidden, map[string]string{
			"error": "webhooks feature is not enabled",
		})
	}

	id := ctx.Param("id")
	logger := LoggerWithRequestID(c.logger, ctx.Request().Context()).With("pipeline_id", id, "method", ctx.Request().Method, "remote_ip", ctx.RealIP())
	logger.Info("webhook.received")

	pipeline, err := c.store.GetPipeline(ctx.Request().Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			logger.Error("webhook.pipeline_not_found")
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "pipeline not found",
			})
		}

		logger.Error("webhook.store_error", "error", err)
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get pipeline: %v", err),
		})
	}

	webhookSecret := ""
	if c.secretsMgr != nil {
		resolvedSecret, getErr := c.secretsMgr.Get(ctx.Request().Context(), secrets.PipelineScope(pipeline.ID), "webhook_secret")
		if getErr == nil {
			webhookSecret = resolvedSecret
		} else if !errors.Is(getErr, secrets.ErrNotFound) {
			logger.Error("webhook.secret_error", "error", getErr)
			return ctx.JSON(http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to get webhook secret: %v", getErr),
			})
		}
	}

	logger = logger.With("pipeline_name", pipeline.Name, "has_webhook_secret", webhookSecret != "")

	body, err := io.ReadAll(ctx.Request().Body)
	if err != nil {
		logger.Error("webhook.read_body_error", "error", err)
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": "failed to read request body",
		})
	}

	logger.Debug("webhook.detecting", "body_bytes", len(body))

	// Log incoming headers at debug level to aid signature debugging.
	for k, v := range ctx.Request().Header {
		if len(v) > 0 {
			logger.Debug("webhook.header", "key", k, "value", v[0])
		}
	}

	event, err := webhooks.Detect(ctx.Request(), body, webhookSecret)
	if err != nil {
		if errors.Is(err, webhooks.ErrUnauthorized) {
			logger.Error("webhook.unauthorized",
				"signature_header", ctx.Request().Header.Get("X-Hub-Signature-256"),
				"slack_signature", ctx.Request().Header.Get("X-Slack-Signature"),
				"generic_signature", ctx.Request().Header.Get("X-Webhook-Signature"),
			)
			return ctx.JSON(http.StatusUnauthorized, map[string]string{
				"error": "webhook signature validation failed",
			})
		}

		logger.Error("webhook.detect_error", "error", err)
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("no webhook provider matched the request: %v", err),
		})
	}

	logger = logger.With("provider", event.Provider, "event_type", event.EventType)
	logger.Info("webhook.detected")

	if !c.execService.CanExecute() {
		logger.Error("webhook.rate_limited",
			"in_flight", c.execService.CurrentInFlight(),
			"max_in_flight", c.execService.MaxInFlight(),
		)
		return ctx.JSON(http.StatusTooManyRequests, map[string]any{
			"error":         "max concurrent executions reached",
			"in_flight":     c.execService.CurrentInFlight(),
			"max_in_flight": c.execService.MaxInFlight(),
		})
	}

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
		logger.Error("webhook.trigger_error", "error", err)
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to trigger pipeline: %v", err),
		})
	}

	logger.Info("webhook.triggered", "run_id", run.ID)

	select {
	case resp := <-responseChan:
		logger.Info("webhook.pipeline_responded", "run_id", run.ID, "status", resp.Status)
		for key, value := range resp.Headers {
			ctx.Response().Header().Set(key, value)
		}

		if resp.Body != "" {
			return ctx.String(resp.Status, resp.Body)
		}

		return ctx.NoContent(resp.Status)

	case <-time.After(c.webhookTimeout):
		logger.Info("webhook.timeout_accepted", "run_id", run.ID)
		return ctx.JSON(http.StatusAccepted, map[string]any{
			"run_id":      run.ID,
			"pipeline_id": pipeline.ID,
			"status":      run.Status,
			"message":     "pipeline execution started",
		})
	}
}

// RegisterRoutes registers all webhook routes on the main router (no auth group).
func (c *APIWebhooksController) RegisterRoutes(router *echo.Echo) {
	router.Any("/api/webhooks/:id", c.Trigger)
}
