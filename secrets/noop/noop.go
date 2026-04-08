package noop

import (
	"context"
	"fmt"

	"github.com/jtarchie/pocketci/secrets"
)

// Noop is a secrets manager that has no backing store.
// Reads return ErrNotFound; writes return an error indicating no backend is configured.
type Noop struct{}

func New() secrets.Manager {
	return &Noop{}
}

func (n *Noop) Get(_ context.Context, _ string, _ string) (string, error) {
	return "", secrets.ErrNotFound
}

func (n *Noop) Set(_ context.Context, _ string, key string, _ string) error {
	return fmt.Errorf("no secrets backend configured: cannot set secret %q", key)
}

func (n *Noop) Delete(_ context.Context, _ string, _ string) error {
	return secrets.ErrNotFound
}

func (n *Noop) ListByScope(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (n *Noop) DeleteByScope(_ context.Context, _ string) error {
	return nil
}

func (n *Noop) Close() error {
	return nil
}
