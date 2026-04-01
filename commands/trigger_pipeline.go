package commands

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/jtarchie/pocketci/client"
)

type TriggerPipeline struct {
	ServerConfig
	Name          string   `arg:""                                                      help:"Name or ID of the pipeline to trigger" required:""`
	Args          []string `help:"Arguments passed to the pipeline (repeatable)"        short:"a"`
	WebhookBody   string   `help:"JSON body for simulated webhook trigger"              name:"webhook-body"`
	WebhookMethod string   `default:"POST"                                              enum:"GET,POST,PUT,PATCH,DELETE"             help:"HTTP method for simulated webhook" name:"webhook-method"`
	WebhookHeader []string `help:"Header for simulated webhook (repeatable, KEY=VALUE)" name:"webhook-header"`
}

func (c *TriggerPipeline) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("pipeline.trigger")

	apiClient := c.NewClient()

	pipeline, err := apiClient.FindPipelineByNameOrID(c.Name)
	if err != nil {
		return err
	}

	logger.Info("pipeline.trigger", "id", pipeline.ID, "name", pipeline.Name)

	body, err := c.buildTriggerRequest()
	if err != nil {
		return err
	}

	result, err := apiClient.TriggerPipeline(pipeline.ID, body)
	if err != nil {
		return err
	}

	fmt.Printf("Pipeline '%s' triggered successfully (run: %s)\n", pipeline.Name, result.RunID)

	return nil
}

func (c *TriggerPipeline) buildTriggerRequest() (client.TriggerRequest, error) {
	if c.WebhookBody != "" || len(c.WebhookHeader) > 0 {
		headers := make(map[string]string)

		for _, h := range c.WebhookHeader {
			k, v, ok := strings.Cut(h, "=")
			if !ok {
				return client.TriggerRequest{}, fmt.Errorf("invalid webhook header %q: expected KEY=VALUE", h)
			}

			headers[k] = v
		}

		return client.TriggerRequest{
			Mode: "webhook",
			Webhook: &client.WebhookSimulation{
				Method:  c.WebhookMethod,
				Body:    c.WebhookBody,
				Headers: headers,
			},
		}, nil
	}

	if len(c.Args) > 0 {
		return client.TriggerRequest{
			Mode: "args",
			Args: c.Args,
		}, nil
	}

	return client.TriggerRequest{}, nil
}
