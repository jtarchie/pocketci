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
	res, err := resources.Get(r.Type)
	if err != nil {
		return fmt.Errorf("failed to get resource: %w", err)
	}

	logger = logger.With("resource", r.Type, "operation", r.Operation, "event", fmt.Sprintf("%s.%s", r.Type, r.Operation))
	logger.Debug("resource.operation.executing")

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()

	response, err := r.executeOperation(ctx, res, input)
	if err != nil {
		return err
	}

	return writeJSONResponse(response)
}

func (r *Resource) executeOperation(ctx context.Context, res resources.Resource, input []byte) (any, error) {
	switch r.Operation {
	case "check":
		return r.executeCheck(ctx, res, input)
	case "in":
		return r.executeIn(ctx, res, input)
	case "out":
		return r.executeOut(ctx, res, input)
	default:
		return nil, fmt.Errorf("unknown operation: %s", r.Operation)
	}
}

func (r *Resource) executeCheck(ctx context.Context, res resources.Resource, input []byte) (any, error) {
	var req resources.CheckRequest

	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("failed to parse check request: %w", err)
	}

	result, err := res.Check(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("check failed: %w", err)
	}

	return result, nil
}

func (r *Resource) executeIn(ctx context.Context, res resources.Resource, input []byte) (any, error) {
	if r.Path == "" {
		return nil, errors.New("path is required for 'in' operation")
	}

	var req resources.InRequest

	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("failed to parse in request: %w", err)
	}

	result, err := res.In(ctx, r.Path, req)
	if err != nil {
		return nil, fmt.Errorf("in failed: %w", err)
	}

	return result, nil
}

func (r *Resource) executeOut(ctx context.Context, res resources.Resource, input []byte) (any, error) {
	if r.Path == "" {
		return nil, errors.New("path is required for 'out' operation")
	}

	var req resources.OutRequest

	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("failed to parse out request: %w", err)
	}

	result, err := res.Out(ctx, r.Path, req)
	if err != nil {
		return nil, fmt.Errorf("out failed: %w", err)
	}

	return result, nil
}

func writeJSONResponse(response any) error {
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
