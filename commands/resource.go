package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/jtarchie/pocketci/resources"
)

// Resource command for executing native resource operations.
// This allows the ci binary to act as a resource executor in containers.
type Resource struct {
	Type      string        `arg:"" help:"Resource type (e.g., git, mock)"`
	Operation string        `arg:"" enum:"check,in,out" help:"Operation to perform (check, in, out)"`
	Path      string        `arg:"" optional:"" help:"Path for in/out operations"`
	Timeout   time.Duration `default:"10m" env:"CI_TIMEOUT" help:"Timeout for the operation"`
}

func (r *Resource) Run(logger *slog.Logger) error {
	// Get the resource
	res, err := resources.Get(r.Type)
	if err != nil {
		return fmt.Errorf("failed to get resource: %w", err)
	}

	logger = logger.With("resource", r.Type, "operation", r.Operation, "event", fmt.Sprintf("%s.%s", r.Type, r.Operation))
	logger.Debug("resource.operation.executing")

	// Read request from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: %w", err)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()

	var response any

	switch r.Operation {
	case "check":
		var req resources.CheckRequest

		err = json.Unmarshal(input, &req)
		if err != nil {
			return fmt.Errorf("failed to parse check request: %w", err)
		}

		response, err = res.Check(ctx, req)
		if err != nil {
			return fmt.Errorf("check failed: %w", err)
		}

	case "in":
		if r.Path == "" {
			return errors.New("path is required for 'in' operation")
		}

		var req resources.InRequest

		err = json.Unmarshal(input, &req)
		if err != nil {
			return fmt.Errorf("failed to parse in request: %w", err)
		}

		response, err = res.In(ctx, r.Path, req)
		if err != nil {
			return fmt.Errorf("in failed: %w", err)
		}

	case "out":
		if r.Path == "" {
			return errors.New("path is required for 'out' operation")
		}

		var req resources.OutRequest

		err = json.Unmarshal(input, &req)
		if err != nil {
			return fmt.Errorf("failed to parse out request: %w", err)
		}

		response, err = res.Out(ctx, r.Path, req)
		if err != nil {
			return fmt.Errorf("out failed: %w", err)
		}

	default:
		return fmt.Errorf("unknown operation: %s", r.Operation)
	}

	// Write response to stdout
	output, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	_, err = os.Stdout.Write(output)
	if err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}

	return nil
}
