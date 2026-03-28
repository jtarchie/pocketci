package commands

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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

	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	client, endpoint := setupAPIClient(serverURL, c.AuthToken, c.ConfigFile)

	pipeline, err := findPipelineByNameOrID(client, endpoint, serverURL, c.Pipeline)
	if err != nil {
		return err
	}

	logger.Info("pipeline.seed-passed", "id", pipeline.ID, "job", c.Job)

	seedURL := endpoint + "/" + pipeline.ID + "/jobs/" + c.Job + "/seed-passed"

	resp, err := client.R().Post(seedURL)
	if err != nil {
		return fmt.Errorf("could not seed job passed status: %w", err)
	}

	if err := checkAuthStatus(resp.StatusCode(), serverURL); err != nil {
		return err
	}

	if resp.StatusCode() == http.StatusNotFound {
		return fmt.Errorf("pipeline %q not found", c.Pipeline)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), resp.String())
	}

	var result struct {
		Job     string `json:"job"`
		RunID   string `json:"run_id"`
		Message string `json:"message"`
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return fmt.Errorf("could not parse response: %w", err)
	}

	fmt.Printf("Seeded passed status for job '%s' in pipeline '%s' (run: %s)\n", c.Job, pipeline.Name, result.RunID)

	return nil
}
