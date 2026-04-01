package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"
	"github.com/jtarchie/pocketci/storage"
)

// ListPipelines returns the paginated list of pipelines.
func (c *Client) ListPipelines() (storage.PaginationResult[storage.Pipeline], error) {
	var result storage.PaginationResult[storage.Pipeline]

	resp, err := c.http.R().Get(c.serverURL + "/api/pipelines")
	if err != nil {
		return result, fmt.Errorf("could not list pipelines: %w", err)
	}

	if err := c.checkAuthStatus(resp.StatusCode()); err != nil {
		return result, err
	}

	if resp.StatusCode() != http.StatusOK {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return result, fmt.Errorf("could not parse pipeline list: %w", err)
	}

	return result, nil
}

// FindPipelineByNameOrID fetches the pipeline list and returns the first
// pipeline matching the given name or ID.
func (c *Client) FindPipelineByNameOrID(nameOrID string) (*storage.Pipeline, error) {
	result, err := c.ListPipelines()
	if err != nil {
		return nil, err
	}

	for _, p := range result.Items {
		if p.ID == nameOrID || p.Name == nameOrID {
			return &p, nil
		}
	}

	return nil, fmt.Errorf("no pipeline found with name or ID %q", nameOrID)
}

// SetPipeline creates or updates a pipeline by name.
func (c *Client) SetPipeline(name string, body SetPipelineRequest) (storage.Pipeline, error) {
	var pipeline storage.Pipeline

	endpoint := c.serverURL + "/api/pipelines/" + url.PathEscape(name)

	resp, err := c.http.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		Put(endpoint)
	if err != nil {
		return pipeline, fmt.Errorf("could not connect to server: %w", err)
	}

	respBody := resp.Body()

	if err := c.checkAuthStatus(resp.StatusCode()); err != nil {
		return pipeline, err
	}

	if resp.StatusCode() != http.StatusOK {
		var errResp map[string]string
		if json.Unmarshal(respBody, &errResp) == nil {
			if msg, ok := errResp["error"]; ok {
				return pipeline, &APIError{StatusCode: resp.StatusCode(), Body: msg}
			}
		}

		return pipeline, &APIError{StatusCode: resp.StatusCode(), Body: string(respBody)}
	}

	if err := json.Unmarshal(respBody, &pipeline); err != nil {
		return pipeline, fmt.Errorf("could not parse response: %w", err)
	}

	return pipeline, nil
}

// DeletePipeline deletes a pipeline by ID.
func (c *Client) DeletePipeline(id string) error {
	resp, err := c.http.R().Delete(c.serverURL + "/api/pipelines/" + id)
	if err != nil {
		return fmt.Errorf("could not delete pipeline: %w", err)
	}

	if err := c.checkAuthStatus(resp.StatusCode()); err != nil {
		return err
	}

	if resp.StatusCode() != http.StatusNoContent {
		return &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	return nil
}

// TriggerPipeline triggers an execution of a pipeline by ID.
func (c *Client) TriggerPipeline(id string, body TriggerRequest) (TriggerResult, error) {
	var result TriggerResult

	resp, err := c.http.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		Post(c.serverURL + "/api/pipelines/" + id + "/trigger")
	if err != nil {
		return result, fmt.Errorf("could not trigger pipeline: %w", err)
	}

	if err := c.checkAuthStatus(resp.StatusCode()); err != nil {
		return result, err
	}

	switch resp.StatusCode() {
	case http.StatusConflict:
		return result, &PipelinePausedError{Name: id}
	case http.StatusTooManyRequests:
		return result, &RateLimitError{}
	case http.StatusNotFound:
		return result, fmt.Errorf("pipeline %q not found", id)
	}

	if resp.StatusCode() != http.StatusAccepted {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return result, fmt.Errorf("could not parse response: %w", err)
	}

	return result, nil
}

// PausePipeline pauses a pipeline by ID.
func (c *Client) PausePipeline(id string) error {
	return c.setPipelineState(id, "pause")
}

// UnpausePipeline unpauses a pipeline by ID.
func (c *Client) UnpausePipeline(id string) error {
	return c.setPipelineState(id, "unpause")
}

func (c *Client) setPipelineState(id, action string) error {
	resp, err := c.http.R().Post(c.serverURL + "/api/pipelines/" + id + "/" + action)
	if err != nil {
		return fmt.Errorf("could not %s pipeline: %w", action, err)
	}

	if err := c.checkAuthStatus(resp.StatusCode()); err != nil {
		return err
	}

	if resp.StatusCode() != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	return nil
}

// SeedJobPassed seeds a job's passed status for a pipeline.
func (c *Client) SeedJobPassed(pipelineID, jobName string) (SeedPassedResult, error) {
	var result SeedPassedResult

	endpoint := c.serverURL + "/api/pipelines/" + pipelineID + "/jobs/" + jobName + "/seed-passed"

	resp, err := c.http.R().Post(endpoint)
	if err != nil {
		return result, fmt.Errorf("could not seed job passed status: %w", err)
	}

	if err := c.checkAuthStatus(resp.StatusCode()); err != nil {
		return result, err
	}

	if resp.StatusCode() == http.StatusNotFound {
		return result, fmt.Errorf("pipeline %q not found", pipelineID)
	}

	if resp.StatusCode() != http.StatusOK {
		return result, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return result, fmt.Errorf("could not parse response: %w", err)
	}

	return result, nil
}

// RunPipeline sends a multipart request to run a pipeline and returns the raw
// response for SSE streaming. The caller must close resp.RawBody().
func (c *Client) RunPipeline(name string, body io.Reader, contentType string, bodySize int64) (*resty.Response, error) {
	endpoint := c.serverURL + "/api/pipelines/" + name + "/run"

	resp, err := c.http.R().
		SetHeader("Content-Type", contentType).
		SetHeader("Content-Length", strconv.FormatInt(bodySize, 10)).
		SetHeader("Accept", "text/event-stream").
		SetBody(body).
		SetDoNotParseResponse(true).
		Post(endpoint)
	if err != nil {
		return nil, fmt.Errorf("could not connect to server: %w", err)
	}

	return resp, nil
}
