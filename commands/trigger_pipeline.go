package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

type TriggerPipeline struct {
	ServerConfig
	Name          string   `arg:""                                                      help:"Name or ID of the pipeline to trigger"                       required:""`
	Args          []string `help:"Arguments passed to the pipeline (repeatable)"        short:"a"`
	WebhookBody   string   `help:"JSON body for simulated webhook trigger"              name:"webhook-body"`
	WebhookMethod string   `default:"POST"                                              enum:"GET,POST,PUT,PATCH,DELETE"                                   help:"HTTP method for simulated webhook" name:"webhook-method"`
	WebhookHeader []string `help:"Header for simulated webhook (repeatable, KEY=VALUE)" name:"webhook-header"`
}

func (c *TriggerPipeline) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("pipeline.trigger")

	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	client, endpoint := setupAPIClient(serverURL, c.AuthToken, c.ConfigFile)

	pipeline, err := findPipelineByNameOrID(client, endpoint, serverURL, c.Name)
	if err != nil {
		return err
	}

	logger.Info("pipeline.trigger", "id", pipeline.ID, "name", pipeline.Name)

	body, err := c.buildTriggerRequest()
	if err != nil {
		return err
	}

	triggerURL := endpoint + "/" + pipeline.ID + "/trigger"

	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		Post(triggerURL)
	if err != nil {
		return fmt.Errorf("could not trigger pipeline: %w", err)
	}

	if err := checkAuthStatus(resp.StatusCode(), serverURL); err != nil {
		return err
	}

	if resp.StatusCode() == http.StatusConflict {
		return fmt.Errorf("pipeline %q is paused", pipeline.Name)
	}

	if resp.StatusCode() == http.StatusTooManyRequests {
		return errors.New("max concurrent executions reached")
	}

	if resp.StatusCode() == http.StatusNotFound {
		return fmt.Errorf("pipeline %q not found", c.Name)
	}

	if resp.StatusCode() != http.StatusAccepted {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), resp.String())
	}

	var result struct {
		RunID   string `json:"run_id"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return fmt.Errorf("could not parse response: %w", err)
	}

	fmt.Printf("Pipeline '%s' triggered successfully (run: %s)\n", pipeline.Name, result.RunID)

	return nil
}

type triggerRequestBody struct {
	Mode    string          `json:"mode,omitempty"`
	Args    []string        `json:"args,omitempty"`
	Webhook *webhookSimBody `json:"webhook,omitempty"`
}

type webhookSimBody struct {
	Method  string            `json:"method"`
	Body    string            `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (c *TriggerPipeline) buildTriggerRequest() (triggerRequestBody, error) {
	if c.WebhookBody != "" || len(c.WebhookHeader) > 0 {
		headers := make(map[string]string)

		for _, h := range c.WebhookHeader {
			k, v, ok := strings.Cut(h, "=")
			if !ok {
				return triggerRequestBody{}, fmt.Errorf("invalid webhook header %q: expected KEY=VALUE", h)
			}

			headers[k] = v
		}

		return triggerRequestBody{
			Mode: "webhook",
			Webhook: &webhookSimBody{
				Method:  c.WebhookMethod,
				Body:    c.WebhookBody,
				Headers: headers,
			},
		}, nil
	}

	if len(c.Args) > 0 {
		return triggerRequestBody{
			Mode: "args",
			Args: c.Args,
		}, nil
	}

	return triggerRequestBody{}, nil
}
