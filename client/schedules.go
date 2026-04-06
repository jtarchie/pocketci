package client

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
)

// ListSchedules returns the schedules for a pipeline.
func (c *Client) ListSchedules(pipelineID string) ([]storage.Schedule, error) {
	resp, err := c.http.R().Get(c.serverURL + "/api/pipelines/" + pipelineID + "/schedules")
	if err != nil {
		return nil, fmt.Errorf("could not list schedules: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	var schedules []storage.Schedule
	err = json.Unmarshal(resp.Body(), &schedules)
	if err != nil {
		return nil, fmt.Errorf("could not parse response: %w", err)
	}

	return schedules, nil
}

// SetScheduleEnabled enables or disables a schedule.
func (c *Client) SetScheduleEnabled(scheduleID string, enabled bool) error {
	resp, err := c.http.R().
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]bool{"enabled": enabled}).
		Put(c.serverURL + "/api/schedules/" + scheduleID + "/enabled")
	if err != nil {
		return fmt.Errorf("could not update schedule: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return err
	}

	if resp.StatusCode() != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	return nil
}
