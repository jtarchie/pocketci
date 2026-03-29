package backwards

import (
	"fmt"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/jtarchie/pocketci/scheduler"
	"github.com/jtarchie/pocketci/storage"
)

// ScheduleFromJob represents a schedule extracted from a job's trigger config.
type ScheduleFromJob struct {
	JobName      string
	ScheduleType storage.ScheduleType
	ScheduleExpr string
}

// ExtractSchedules parses a pipeline YAML config and returns all schedule
// declarations found in job triggers.
func ExtractSchedules(content string) ([]ScheduleFromJob, error) {
	var config Config

	err := yaml.Unmarshal([]byte(content), &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pipeline config: %w", err)
	}

	var schedules []ScheduleFromJob

	for _, job := range config.Jobs {
		if job.Triggers == nil || job.Triggers.Schedule == nil {
			continue
		}

		sched := job.Triggers.Schedule

		var schedType storage.ScheduleType

		var schedExpr string

		switch {
		case sched.Cron != "":
			schedType = storage.ScheduleTypeCron
			schedExpr = sched.Cron
		case sched.Every != "":
			schedType = storage.ScheduleTypeInterval
			schedExpr = sched.Every
		default:
			return nil, fmt.Errorf("job %q: schedule trigger must specify either cron or every", job.Name)
		}

		// Validate the expression using a fixed reference time.
		_, err := scheduler.ComputeNextRun(schedType, schedExpr, time.Unix(0, 0))
		if err != nil {
			return nil, fmt.Errorf("job %q: %w", job.Name, err)
		}

		schedules = append(schedules, ScheduleFromJob{
			JobName:      job.Name,
			ScheduleType: schedType,
			ScheduleExpr: schedExpr,
		})
	}

	return schedules, nil
}
