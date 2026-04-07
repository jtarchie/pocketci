package commands

import (
	"fmt"
	"log/slog"
)

// SeedPassed seeds a job's passed status so that cross-run `passed` constraints
// referencing the named job are immediately satisfied.
type SeedPassed struct {
	ServerConfig
	Pipeline string `arg:"" help:"Pipeline name or ID"        required:""`
	Job      string `arg:"" help:"Job name to seed as passed" required:""`
}

func (c *SeedPassed) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("pipeline.seed-passed")

	apiClient := c.NewClient()

	pipeline, err := apiClient.FindPipelineByNameOrID(c.Pipeline)
	if err != nil {
		return fmt.Errorf("find pipeline: %w", err)
	}

	logger.Info("pipeline.seed-passed", "id", pipeline.ID, "job", c.Job)

	result, err := apiClient.SeedJobPassed(pipeline.ID, c.Job)
	if err != nil {
		return fmt.Errorf("seed job passed: %w", err)
	}

	fmt.Printf("Seeded passed status for job '%s' in pipeline '%s' (run: %s)\n", c.Job, pipeline.Name, result.RunID)

	return nil
}
