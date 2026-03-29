package commands

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Schedule groups schedule management subcommands.
type Schedule struct {
	Ls      ListSchedules   `cmd:"" help:"List schedules for a pipeline"`
	Pause   PauseSchedule   `cmd:"" help:"Pause a schedule"`
	Unpause UnpauseSchedule `cmd:"" help:"Unpause a schedule"`
}

type scheduleResponse struct {
	ID           string     `json:"id"`
	PipelineID   string     `json:"pipeline_id"`
	Name         string     `json:"name"`
	ScheduleType string     `json:"schedule_type"`
	ScheduleExpr string     `json:"schedule_expr"`
	JobName      string     `json:"job_name"`
	Enabled      bool       `json:"enabled"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
}

// ListSchedules lists all schedules for a pipeline.
type ListSchedules struct {
	ServerConfig
	Name string `arg:"" help:"Name or ID of the pipeline" required:""`
}

func (c *ListSchedules) Run(_ *slog.Logger) error {
	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	client, endpoint := setupAPIClient(serverURL, c.AuthToken, c.ConfigFile)

	pipeline, err := findPipelineByNameOrID(client, endpoint, serverURL, c.Name)
	if err != nil {
		return err
	}

	resp, err := client.R().Get(endpoint + "/" + pipeline.ID + "/schedules")
	if err != nil {
		return fmt.Errorf("could not list schedules: %w", err)
	}

	if err := checkAuthStatus(resp.StatusCode(), serverURL); err != nil {
		return err
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), resp.String())
	}

	var schedules []scheduleResponse
	if err := json.Unmarshal(resp.Body(), &schedules); err != nil {
		return fmt.Errorf("could not parse response: %w", err)
	}

	if len(schedules) == 0 {
		fmt.Printf("No schedules for pipeline '%s'\n", pipeline.Name)

		return nil
	}

	for _, s := range schedules {
		status := "enabled"
		if !s.Enabled {
			status = "paused"
		}

		nextRun := "never"
		if s.NextRunAt != nil {
			nextRun = s.NextRunAt.Format(time.RFC3339)
		}

		fmt.Printf("%-20s %-10s %-12s %-8s next: %s\n",
			s.Name, s.ScheduleType, s.ScheduleExpr, status, nextRun)
	}

	return nil
}

// PauseSchedule pauses a schedule by name.
type PauseSchedule struct {
	ServerConfig
	Pipeline string `arg:"" help:"Name or ID of the pipeline" required:""`
	Name     string `arg:"" help:"Schedule name to pause"     required:""`
}

func (c *PauseSchedule) Run(logger *slog.Logger) error {
	return setScheduleEnabled(c.ServerConfig, c.Pipeline, c.Name, false, logger)
}

// UnpauseSchedule unpauses a schedule by name.
type UnpauseSchedule struct {
	ServerConfig
	Pipeline string `arg:"" help:"Name or ID of the pipeline" required:""`
	Name     string `arg:"" help:"Schedule name to unpause"   required:""`
}

func (c *UnpauseSchedule) Run(logger *slog.Logger) error {
	return setScheduleEnabled(c.ServerConfig, c.Pipeline, c.Name, true, logger)
}

func setScheduleEnabled(cfg ServerConfig, pipelineName, scheduleName string, enabled bool, _ *slog.Logger) error {
	serverURL := strings.TrimSuffix(cfg.ServerURL, "/")
	client, endpoint := setupAPIClient(serverURL, cfg.AuthToken, cfg.ConfigFile)

	pipeline, err := findPipelineByNameOrID(client, endpoint, serverURL, pipelineName)
	if err != nil {
		return err
	}

	// First, list schedules to find the one by name
	resp, err := client.R().Get(endpoint + "/" + pipeline.ID + "/schedules")
	if err != nil {
		return fmt.Errorf("could not list schedules: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), resp.String())
	}

	var schedules []scheduleResponse
	if err := json.Unmarshal(resp.Body(), &schedules); err != nil {
		return fmt.Errorf("could not parse response: %w", err)
	}

	var scheduleID string

	for _, s := range schedules {
		if s.Name == scheduleName {
			scheduleID = s.ID

			break
		}
	}

	if scheduleID == "" {
		return fmt.Errorf("schedule %q not found for pipeline %q", scheduleName, pipeline.Name)
	}

	// Build schedule endpoint from server URL directly.
	scheduleEndpoint := serverURL + "/api/schedules"

	resp, err = client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]bool{"enabled": enabled}).
		Put(scheduleEndpoint + "/" + scheduleID + "/enabled")
	if err != nil {
		return fmt.Errorf("could not update schedule: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), resp.String())
	}

	action := "paused"
	if enabled {
		action = "unpaused"
	}

	fmt.Printf("Schedule '%s' %s for pipeline '%s'\n", scheduleName, action, pipeline.Name)

	return nil
}
