package client

import "fmt"

// AuthRequiredError is returned when the server responds with 401 Unauthorized.
type AuthRequiredError struct {
	ServerURL string
}

func (e *AuthRequiredError) Error() string {
	return fmt.Sprintf(
		"authentication required: server %s returned 401 Unauthorized\n\n"+
			"Please log in first:\n"+
			"  ci login -s %s\n\n"+
			"Or provide a token directly:\n"+
			"  export CI_AUTH_TOKEN=<token>",
		e.ServerURL, e.ServerURL,
	)
}

// AccessDeniedError is returned when the server responds with 403 Forbidden.
type AccessDeniedError struct {
	ServerURL string
}

func (e *AccessDeniedError) Error() string {
	return fmt.Sprintf(
		"access denied: server %s returned 403 Forbidden\n\n"+
			"Your account does not have permission for this operation.\n"+
			"Contact your administrator for access.",
		e.ServerURL,
	)
}

// APIError is returned for non-auth HTTP errors from the server.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.StatusCode, e.Body)
}

// PipelinePausedError is returned when a trigger is attempted on a paused pipeline.
type PipelinePausedError struct {
	Name string
}

func (e *PipelinePausedError) Error() string {
	return fmt.Sprintf("pipeline %q is paused", e.Name)
}

// RateLimitError is returned when the server responds with 429 Too Many Requests.
type RateLimitError struct{}

func (e *RateLimitError) Error() string {
	return "max concurrent executions reached"
}

// checkAuthStatus returns a typed error for 401/403 status codes, nil otherwise.
func (c *Client) checkAuthStatus(statusCode int) error {
	switch statusCode {
	case 401:
		return &AuthRequiredError{ServerURL: c.serverURL}
	case 403:
		return &AccessDeniedError{ServerURL: c.serverURL}
	default:
		return nil
	}
}
