package client

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
)

// ListGates returns all approval gates for a pipeline run.
func (c *Client) ListGates(runID string) ([]storage.Gate, error) {
	var result []storage.Gate

	resp, err := c.http.R().Get(c.serverURL + "/api/runs/" + runID + "/gates")
	if err != nil {
		return result, fmt.Errorf("could not list gates: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return result, err
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

// ApproveGate approves a pending approval gate.
func (c *Client) ApproveGate(gateID string) (storage.Gate, error) {
	return c.resolveGate(gateID, "approve")
}

// RejectGate rejects a pending approval gate.
func (c *Client) RejectGate(gateID string) (storage.Gate, error) {
	return c.resolveGate(gateID, "reject")
}

func (c *Client) resolveGate(gateID, action string) (storage.Gate, error) {
	var result storage.Gate

	resp, err := c.http.R().Post(c.serverURL + "/api/gates/" + gateID + "/" + action)
	if err != nil {
		return result, fmt.Errorf("could not %s gate: %w", action, err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return result, err
	}

	switch resp.StatusCode() {
	case http.StatusNotFound:
		return result, &NotFoundError{Resource: "gate", ID: gateID}
	case http.StatusConflict:
		return result, &GateAlreadyResolvedError{GateID: gateID}
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
