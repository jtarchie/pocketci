package commands

import (
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
)

type ListPipelines struct {
	ServerConfig
}

func (c *ListPipelines) Run(_ *slog.Logger) error {
	apiClient := c.NewClient()

	result, err := apiClient.ListPipelines()
	if err != nil {
		return fmt.Errorf("list pipelines: %w", err)
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

	flushErr := w.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush output: %w", flushErr)
	}

	return nil
}
