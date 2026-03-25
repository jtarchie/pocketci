package commands

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/jtarchie/pocketci/storage"
)

type TriggerPipeline struct {
	Name          string   `arg:""              help:"Name or ID of the pipeline to trigger"                    required:""`
	ServerURL     string   `env:"CI_SERVER_URL" help:"URL of the CI server"                                    required:"" short:"s"`
	AuthToken     string   `env:"CI_AUTH_TOKEN" help:"Bearer token for OAuth-authenticated servers"             short:"t"`
	ConfigFile    string   `env:"CI_AUTH_CONFIG" help:"Path to auth config file (default: ~/.pocketci/auth.config)" short:"c"`
	Args          []string `help:"Arguments passed to the pipeline (repeatable)" short:"a"`
	WebhookBody   string   `help:"JSON body for simulated webhook trigger"       name:"webhook-body"`
	WebhookMethod string   `help:"HTTP method for simulated webhook"             name:"webhook-method" default:"POST" enum:"GET,POST,PUT,PATCH,DELETE"`
	WebhookHeader []string `help:"Header for simulated webhook (repeatable, KEY=VALUE)" name:"webhook-header"`
}

func (c *TriggerPipeline) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("pipeline.trigger")

	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	client, endpoint := c.setupClient(serverURL)

	pipeline, err := c.resolvePipeline(client, endpoint, serverURL)
	if err != nil {
		return err
	}

	logger.Info("pipeline.trigger", "id", pipeline.ID, "name", pipeline.Name)

	body := c.buildTriggerRequest()

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
		return fmt.Errorf("max concurrent executions reached")
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

func (c *TriggerPipeline) setupClient(serverURL string) (*resty.Client, string) {
	endpoint := serverURL + "/api/pipelines"
	client := resty.New()

	if parsed, err := url.Parse(serverURL); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		client.SetBasicAuth(parsed.User.Username(), password)
		parsed.User = nil
		endpoint = parsed.String() + "/api/pipelines"
	}

	token := ResolveAuthToken(c.AuthToken, c.ConfigFile, c.ServerURL)
	if token != "" {
		client.SetAuthToken(token)
	}

	return client, endpoint
}

func (c *TriggerPipeline) resolvePipeline(client *resty.Client, endpoint, serverURL string) (*storage.Pipeline, error) {
	listResp, err := client.R().Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("could not list pipelines: %w", err)
	}

	if err := checkAuthStatus(listResp.StatusCode(), serverURL); err != nil {
		return nil, err
	}

	if listResp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("server error listing pipelines (%d): %s", listResp.StatusCode(), listResp.String())
	}

	var result storage.PaginationResult[storage.Pipeline]
	if err := json.Unmarshal(listResp.Body(), &result); err != nil {
		return nil, fmt.Errorf("could not parse pipeline list: %w", err)
	}

	for _, p := range result.Items {
		if p.ID == c.Name || p.Name == c.Name {
			return &p, nil
		}
	}

	return nil, fmt.Errorf("no pipeline found with name or ID %q", c.Name)
}

type triggerRequestBody struct {
	Mode    string           `json:"mode,omitempty"`
	Args    []string         `json:"args,omitempty"`
	Webhook *webhookSimBody  `json:"webhook,omitempty"`
}

type webhookSimBody struct {
	Method  string            `json:"method"`
	Body    string            `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (c *TriggerPipeline) buildTriggerRequest() triggerRequestBody {
	if c.WebhookBody != "" || len(c.WebhookHeader) > 0 {
		headers := make(map[string]string)
		for _, h := range c.WebhookHeader {
			k, v, _ := strings.Cut(h, "=")
			headers[k] = v
		}

		var hdrs map[string]string
		if len(headers) > 0 {
			hdrs = headers
		}

		return triggerRequestBody{
			Mode: "webhook",
			Webhook: &webhookSimBody{
				Method:  c.WebhookMethod,
				Body:    c.WebhookBody,
				Headers: hdrs,
			},
		}
	}

	if len(c.Args) > 0 {
		return triggerRequestBody{
			Mode: "args",
			Args: c.Args,
		}
	}

	return triggerRequestBody{}
}
