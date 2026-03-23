package commands

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/jtarchie/pocketci/storage"
)

type PausePipeline struct {
	Name       string `arg:""               help:"Name or ID of the pipeline to pause"                         required:""`
	ServerURL  string `env:"CI_SERVER_URL"  help:"URL of the CI server"                                        required:"" short:"s"`
	AuthToken  string `env:"CI_AUTH_TOKEN"  help:"Bearer token for OAuth-authenticated servers"                short:"t"`
	ConfigFile string `env:"CI_AUTH_CONFIG" help:"Path to auth config file (default: ~/.pocketci/auth.config)" short:"c"`
}

func (c *PausePipeline) Run(logger *slog.Logger) error {
	return setPipelinePaused(logger, c.Name, c.ServerURL, c.AuthToken, c.ConfigFile, true)
}

type UnpausePipeline struct {
	Name       string `arg:""               help:"Name or ID of the pipeline to unpause"                       required:""`
	ServerURL  string `env:"CI_SERVER_URL"  help:"URL of the CI server"                                        required:"" short:"s"`
	AuthToken  string `env:"CI_AUTH_TOKEN"  help:"Bearer token for OAuth-authenticated servers"                short:"t"`
	ConfigFile string `env:"CI_AUTH_CONFIG" help:"Path to auth config file (default: ~/.pocketci/auth.config)" short:"c"`
}

func (c *UnpausePipeline) Run(logger *slog.Logger) error {
	return setPipelinePaused(logger, c.Name, c.ServerURL, c.AuthToken, c.ConfigFile, false)
}

func setupAPIClient(serverURL, authToken, configFile string) (*resty.Client, string) {
	endpoint := serverURL + "/api/pipelines"
	client := resty.New()

	if parsed, err := url.Parse(serverURL); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		client.SetBasicAuth(parsed.User.Username(), password)
		parsed.User = nil
		endpoint = parsed.String() + "/api/pipelines"
	}

	token := ResolveAuthToken(authToken, configFile, serverURL)
	if token != "" {
		client.SetAuthToken(token)
	}

	return client, endpoint
}

func findPipelineByNameOrID(client *resty.Client, endpoint, serverURL, name string) (*storage.Pipeline, error) {
	listResp, err := client.R().Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("could not list pipelines: %w", err)
	}

	if err := checkAuthStatus(listResp.StatusCode(), serverURL); err != nil {
		return nil, err
	}

	if listResp.StatusCode() != 200 {
		return nil, fmt.Errorf("server error listing pipelines (%d): %s", listResp.StatusCode(), listResp.String())
	}

	var result storage.PaginationResult[storage.Pipeline]
	if err := json.Unmarshal(listResp.Body(), &result); err != nil {
		return nil, fmt.Errorf("could not parse pipeline list: %w", err)
	}

	for _, p := range result.Items {
		if p.ID == name || p.Name == name {
			return &p, nil
		}
	}

	return nil, fmt.Errorf("no pipeline found with name or ID %q", name)
}

func setPipelinePaused(logger *slog.Logger, name, serverURL, authToken, configFile string, paused bool) error {
	action := "pause"
	if !paused {
		action = "unpause"
	}

	logger = logger.WithGroup("pipeline." + action)

	serverURL = strings.TrimSuffix(serverURL, "/")
	client, endpoint := setupAPIClient(serverURL, authToken, configFile)

	logger.Info("pipeline.list")

	matched, err := findPipelineByNameOrID(client, endpoint, serverURL, name)
	if err != nil {
		return err
	}

	logger.Info("pipeline."+action, "id", matched.ID, "name", matched.Name)

	resp, err := client.R().Post(endpoint + "/" + matched.ID + "/" + action)
	if err != nil {
		return fmt.Errorf("could not %s pipeline %q (%s): %w", action, matched.Name, matched.ID, err)
	}

	if err := checkAuthStatus(resp.StatusCode(), serverURL); err != nil {
		return err
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("server error (%d): %s", resp.StatusCode(), resp.String())
	}

	pastTense := "paused"
	if !paused {
		pastTense = "unpaused"
	}

	fmt.Printf("Pipeline '%s' %s successfully (ID: %s)\n", matched.Name, pastTense, matched.ID)

	return nil
}
