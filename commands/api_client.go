package commands

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/go-resty/resty/v2"
	"github.com/jtarchie/pocketci/storage"
)

// setupAPIClient creates an authenticated resty client and returns it along
// with the base pipelines endpoint (serverURL + "/api/pipelines"). It handles
// embedded URL credentials and auth token resolution.
func setupAPIClient(serverURL, authToken, configFile string) (*resty.Client, string) {
	endpoint := serverURL + "/api/pipelines"
	client := resty.New()

	if parsed, err := url.Parse(serverURL); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		client.SetBasicAuth(parsed.User.Username(), password)
		parsed.User = nil
		endpoint = parsed.String() + "/api/pipelines"
	}

	token := ResolveAuthToken(authToken, configFile, serverURL)
	if token != "" {
		client.SetAuthToken(token)
	}

	return client, endpoint
}

// findPipelineByNameOrID fetches the pipeline list from the server and returns
// the first pipeline matching the given name or ID.
func findPipelineByNameOrID(client *resty.Client, endpoint, serverURL, name string) (*storage.Pipeline, error) {
	listResp, err := client.R().Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("could not list pipelines: %w", err)
	}

	if err := checkAuthStatus(listResp.StatusCode(), serverURL); err != nil {
		return nil, err
	}

	if listResp.StatusCode() != 200 {
		return nil, fmt.Errorf("server error listing pipelines (%d): %s", listResp.StatusCode(), listResp.String())
	}

	var result storage.PaginationResult[storage.Pipeline]
	if err := json.Unmarshal(listResp.Body(), &result); err != nil {
		return nil, fmt.Errorf("could not parse pipeline list: %w", err)
	}

	for _, p := range result.Items {
		if p.ID == name || p.Name == name {
			return &p, nil
		}
	}

	return nil, fmt.Errorf("no pipeline found with name or ID %q", name)
}
