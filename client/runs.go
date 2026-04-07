package client

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
)

// GetRunStatus returns the current status of a pipeline run.
func (c *Client) GetRunStatus(runID string) (storage.PipelineRun, error) {
	var result storage.PipelineRun

	resp, err := c.http.R().Get(c.serverURL + "/api/runs/" + runID + "/status")
	if err != nil {
		return result, fmt.Errorf("could not get run status: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return result, err
	}

	if resp.StatusCode() == http.StatusNotFound {
		return result, &NotFoundError{Resource: "run", ID: runID}
	}

	if resp.StatusCode() != http.StatusOK {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	err = json.Unmarshal(resp.Body(), &result)
	if err != nil {
		return result, fmt.Errorf("could not parse response: %w", err)
	}

	return result, nil
}

// GetRunTasks returns all tasks recorded for a pipeline run.
func (c *Client) GetRunTasks(runID string) ([]RunTask, error) {
	var result []RunTask

	resp, err := c.http.R().Get(c.serverURL + "/api/runs/" + runID + "/tasks")
	if err != nil {
		return result, fmt.Errorf("could not get run tasks: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return result, err
	}

	if resp.StatusCode() == http.StatusNotFound {
		return result, &NotFoundError{Resource: "run", ID: runID}
	}

	if resp.StatusCode() != http.StatusOK {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	err = json.Unmarshal(resp.Body(), &result)
	if err != nil {
		return result, fmt.Errorf("could not parse response: %w", err)
	}

	return result, nil
}

// StopRun requests cancellation of an in-flight pipeline run.
func (c *Client) StopRun(runID string) (RunActionResult, error) {
	var result RunActionResult

	resp, err := c.http.R().Post(c.serverURL + "/api/runs/" + runID + "/stop")
	if err != nil {
		return result, fmt.Errorf("could not stop run: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return result, err
	}

	if resp.StatusCode() == http.StatusNotFound {
		return result, &NotFoundError{Resource: "run", ID: runID}
	}

	if resp.StatusCode() != http.StatusOK {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	err = json.Unmarshal(resp.Body(), &result)
	if err != nil {
		return result, fmt.Errorf("could not parse response: %w", err)
	}

	return result, nil
}

// ResumeRun resumes a failed or aborted pipeline run.
func (c *Client) ResumeRun(runID string) (RunActionResult, error) {
	var result RunActionResult

	resp, err := c.http.R().Post(c.serverURL + "/api/runs/" + runID + "/resume")
	if err != nil {
		return result, fmt.Errorf("could not resume run: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return result, err
	}

	switch resp.StatusCode() {
	case http.StatusNotFound:
		return result, &NotFoundError{Resource: "run", ID: runID}
	case http.StatusTooManyRequests:
		return result, &RateLimitError{}
	}

	if resp.StatusCode() != http.StatusOK {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	err = json.Unmarshal(resp.Body(), &result)
	if err != nil {
		return result, fmt.Errorf("could not parse response: %w", err)
	}

	return result, nil
}

// ShareRun creates a signed share token for a run and returns the share path.
func (c *Client) ShareRun(runID string) (ShareResult, error) {
	var result ShareResult

	resp, err := c.http.R().Post(c.serverURL + "/api/runs/" + runID + "/share")
	if err != nil {
		return result, fmt.Errorf("could not share run: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return result, err
	}

	if resp.StatusCode() == http.StatusNotFound {
		return result, &NotFoundError{Resource: "run", ID: runID}
	}

	if resp.StatusCode() != http.StatusOK {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	err = json.Unmarshal(resp.Body(), &result)
	if err != nil {
		return result, fmt.Errorf("could not parse response: %w", err)
	}

	return result, nil
}
