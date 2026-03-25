package commands

import (
	"fmt"
	"log/slog"
	"strings"
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
