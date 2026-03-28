package commands

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/jtarchie/pocketci/storage"
)

type ListPipelines struct {
	ServerConfig
}

func (c *ListPipelines) Run(_ *slog.Logger) error {
	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	client, endpoint := setupAPIClient(serverURL, c.AuthToken, c.ConfigFile)

	resp, err := client.R().Get(endpoint)
	if err != nil {
		return fmt.Errorf("could not list pipelines: %w", err)
	}

	if err := checkAuthStatus(resp.StatusCode(), serverURL); err != nil {
		return err
	}

	if resp.StatusCode() != 200 {
		return fmt.Errorf("server error listing pipelines (%d): %s", resp.StatusCode(), resp.String())
	}

	var result storage.PaginationResult[storage.Pipeline]
	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return fmt.Errorf("could not parse pipeline list: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintln(w, "NAME\tDRIVER\tPAUSED\tCREATED")

	for _, p := range result.Items {
		paused := "no"
		if p.Paused {
			paused = "yes"
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.Driver, paused, p.CreatedAt.Format("Jan 02, 2006 15:04"))
	}

	return w.Flush()
}
