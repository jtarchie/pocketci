package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// BeginDeviceFlow starts the OAuth2 device flow and returns the code and login URL.
func (c *Client) BeginDeviceFlow() (DeviceFlowResult, error) {
	var result DeviceFlowResult

	resp, err := c.http.R().Post(c.serverURL + "/auth/cli/begin")
	if err != nil {
		return result, fmt.Errorf("could not connect to server: %w", err)
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

// PollDeviceFlow polls for a device flow token. Returns (token, done, error).
// When done is false and error is nil, the authorization is still pending.
func (c *Client) PollDeviceFlow(code string) (string, bool, error) {
	pollEndpoint := c.serverURL + "/auth/cli/poll?code=" + url.QueryEscape(code)

	resp, err := c.http.R().Get(pollEndpoint)
	if err != nil {
		return "", false, nil //nolint:nilerr // transient network errors are retried by the caller
	}

	switch resp.StatusCode() {
	case http.StatusAccepted:
		return "", false, nil
	case http.StatusGone:
		return "", false, errors.New("authentication code expired, please try again")
	case http.StatusOK:
		// success, parse token below
	default:
		return "", false, fmt.Errorf("unexpected response (%d): %s", resp.StatusCode(), resp.String())
	}

	var tokenResult struct {
		Token string `json:"token"`
	}

	err = json.Unmarshal(resp.Body(), &tokenResult)
	if err != nil {
		return "", false, fmt.Errorf("could not parse token response: %w", err)
	}

	return tokenResult.Token, true, nil
}
