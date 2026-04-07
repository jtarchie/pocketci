package commands

import (
	"fmt"
	"log/slog"
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

	apiClient := cfg.NewClient()

	logger.Info("pipeline.list")

	matched, err := apiClient.FindPipelineByNameOrID(name)
	if err != nil {
		return fmt.Errorf("find pipeline: %w", err)
	}

	logger.Info("pipeline."+action, "id", matched.ID, "name", matched.Name)

	if paused {
		err = apiClient.PausePipeline(matched.ID)
	} else {
		err = apiClient.UnpausePipeline(matched.ID)
	}

	if err != nil {
		return fmt.Errorf("could not %s pipeline %q (%s): %w", action, matched.Name, matched.ID, err)
	}

	pastTense := "paused"
	if !paused {
		pastTense = "unpaused"
	}

	fmt.Printf("Pipeline '%s' %s successfully (ID: %s)\n", matched.Name, pastTense, matched.ID)

	return nil
}
