package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// triggerRequest is the optional JSON body for POST /api/pipelines/:id/trigger.
type triggerRequest struct {
	Mode    string          `json:"mode"`    // "" or "manual" (default), "args", "webhook"
	Args    []string        `json:"args"`    // for mode="args"
	Jobs    []string        `json:"jobs"`    // optional, restricts execution to these jobs
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
		return c.execService.TriggerPipelineWithJobs(ctx.Request().Context(), pipeline, req.Args, req.Jobs)

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
		return c.execService.TriggerPipelineWithJobs(ctx.Request().Context(), pipeline, nil, req.Jobs)
	}
}

// triggerNotFound writes an appropriate 404 response for HTMX or JSON clients.
func (c *APIPipelinesController) triggerNotFound(ctx *echo.Context) error {
	return respondHTMXOrJSON(ctx, http.StatusNotFound,
		"trigger pipeline not found", "Pipeline not found",
		map[string]string{"error": "pipeline not found"})
}

// triggerPipelinePaused writes an appropriate 409 response for HTMX or JSON clients.
func (c *APIPipelinesController) triggerPipelinePaused(ctx *echo.Context) error {
	return respondHTMXOrJSON(ctx, http.StatusConflict,
		"trigger paused", "Pipeline is paused",
		map[string]string{"error": "pipeline is paused"})
}

// triggerQueueFull writes an appropriate 429 response for HTMX or JSON clients.
func (c *APIPipelinesController) triggerQueueFull(ctx *echo.Context) error {
	return respondHTMXOrJSON(ctx, http.StatusTooManyRequests,
		"trigger queue full", "Execution queue is full",
		map[string]any{
			"error":          "execution queue is full",
			"in_flight":      c.execService.CurrentInFlight(),
			"max_in_flight":  c.execService.MaxInFlight(),
			"max_queue_size": c.execService.MaxQueueSize(),
		})
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
