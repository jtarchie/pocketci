package commands

import (
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/storage"
)

type DeletePipeline struct {
	ServerConfig
	Name string `arg:"" help:"Name or ID of the pipeline to delete" required:""`
}

func (c *DeletePipeline) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("pipeline.delete")

	apiClient := c.NewClient()

	logger.Info("pipeline.list")

	result, err := apiClient.ListPipelines()
	if err != nil {
		return err
	}

	matched := c.filterMatches(result.Items)
	if len(matched) == 0 {
		return fmt.Errorf("no pipeline found with name or ID %q", c.Name)
	}

	for _, p := range matched {
		logger.Info("pipeline.delete", "id", p.ID, "name", p.Name)

		if err := apiClient.DeletePipeline(p.ID); err != nil {
			return fmt.Errorf("could not delete pipeline %q (%s): %w", p.Name, p.ID, err)
		}

		fmt.Printf("Pipeline '%s' deleted successfully (ID: %s)\n", p.Name, p.ID)
	}

	return nil
}

func (c *DeletePipeline) filterMatches(pipelines []storage.Pipeline) []storage.Pipeline {
	var matched []storage.Pipeline

	for _, p := range pipelines {
		if p.ID == c.Name || p.Name == c.Name {
			matched = append(matched, p)
		}
	}

	return matched
}
