package client

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ListDrivers returns the list of allowed container drivers on the server.
func (c *Client) ListDrivers() ([]string, error) {
	resp, err := c.http.R().Get(c.serverURL + "/api/drivers")
	if err != nil {
		return nil, fmt.Errorf("could not list drivers: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	var body struct {
		Drivers []string `json:"drivers"`
	}

	err = json.Unmarshal(resp.Body(), &body)
	if err != nil {
		return nil, fmt.Errorf("could not parse response: %w", err)
	}

	return body.Drivers, nil
}

// ListFeatures returns the list of enabled features on the server.
func (c *Client) ListFeatures() ([]string, error) {
	resp, err := c.http.R().Get(c.serverURL + "/api/features")
	if err != nil {
		return nil, fmt.Errorf("could not list features: %w", err)
	}

	err = c.checkAuthStatus(resp.StatusCode())
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode(), Body: resp.String()}
	}

	var body struct {
		Features []string `json:"features"`
	}

	err = json.Unmarshal(resp.Body(), &body)
	if err != nil {
		return nil, fmt.Errorf("could not parse response: %w", err)
	}

	return body.Features, nil
}
