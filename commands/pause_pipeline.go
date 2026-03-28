package commands

import (
	"fmt"
	"log/slog"
	"strings"
)

type PausePipeline struct {
	ServerConfig
	Name string `arg:"" help:"Name or ID of the pipeline to pause" required:""`
}

func (c *PausePipeline) Run(logger *slog.Logger) error {
	return setPipelinePaused(logger, c.Name, c.ServerConfig, true)
}

type UnpausePipeline struct {
	ServerConfig
	Name string `arg:"" help:"Name or ID of the pipeline to unpause" required:""`
}

func (c *UnpausePipeline) Run(logger *slog.Logger) error {
	return setPipelinePaused(logger, c.Name, c.ServerConfig, false)
}

func setPipelinePaused(logger *slog.Logger, name string, cfg ServerConfig, paused bool) error {
	action := "pause"
	if !paused {
		action = "unpause"
	}

	logger = logger.WithGroup("pipeline." + action)

	serverURL := strings.TrimSuffix(cfg.ServerURL, "/")
	client, endpoint := setupAPIClient(serverURL, cfg.AuthToken, cfg.ConfigFile)

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
