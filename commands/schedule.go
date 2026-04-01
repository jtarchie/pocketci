package commands

import (
	"fmt"
	"log/slog"
	"time"
)

// Schedule groups schedule management subcommands.
type Schedule struct {
	Ls      ListSchedules   `cmd:"" help:"List schedules for a pipeline"`
	Pause   PauseSchedule   `cmd:"" help:"Pause a schedule"`
	Unpause UnpauseSchedule `cmd:"" help:"Unpause a schedule"`
}

// ListSchedules lists all schedules for a pipeline.
type ListSchedules struct {
	ServerConfig
	Name string `arg:"" help:"Name or ID of the pipeline" required:""`
}

func (c *ListSchedules) Run(_ *slog.Logger) error {
	apiClient := c.NewClient()

	pipeline, err := apiClient.FindPipelineByNameOrID(c.Name)
	if err != nil {
		return err
	}

	schedules, err := apiClient.ListSchedules(pipeline.ID)
	if err != nil {
		return err
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
	apiClient := cfg.NewClient()

	pipeline, err := apiClient.FindPipelineByNameOrID(pipelineName)
	if err != nil {
		return err
	}

	schedules, err := apiClient.ListSchedules(pipeline.ID)
	if err != nil {
		return err
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

	if err := apiClient.SetScheduleEnabled(scheduleID, enabled); err != nil {
		return err
	}

	action := "paused"
	if enabled {
		action = "unpaused"
	}

	fmt.Printf("Schedule '%s' %s for pipeline '%s'\n", scheduleName, action, pipeline.Name)

	return nil
}
