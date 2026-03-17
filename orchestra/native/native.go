package native

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jtarchie/pocketci/orchestra"
)

// Config holds configuration for the native driver.
type Config struct {
	Namespace string // Per-execution namespace identifier
}

type Native struct {
	logger    *slog.Logger
	namespace string
	path      string
}

// Close implements orchestra.Driver.
func (n *Native) Close() error {
	err := os.RemoveAll(n.path)
	if err != nil {
		return fmt.Errorf("failed to remove temp dir: %w", err)
	}

	return nil
}

func New(cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	path, err := os.MkdirTemp("", cfg.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	return &Native{
		logger:    logger,
		namespace: cfg.Namespace,
		path:      path,
	}, nil
}

func (n *Native) Name() string {
	return "native"
}

// GetContainer attempts to find an existing container.
// Native driver does not support container reattachment since processes are not persistent.
// Always returns ErrContainerNotFound.
func (n *Native) GetContainer(_ context.Context, _ string) (orchestra.Container, error) {
	return nil, orchestra.ErrContainerNotFound
}

var (
	_ orchestra.Driver          = &Native{}
	_ orchestra.Container       = &Container{}
	_ orchestra.ContainerStatus = &Status{}
	_ orchestra.Volume          = &Volume{}
)
