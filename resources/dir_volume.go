package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirVolumeContext implements VolumeContext backed by a host filesystem directory.
// OpenSandbox is not supported — use this when no driver is available (e.g. JS runner, tests).
type DirVolumeContext struct {
	Dir string
}

func (v *DirVolumeContext) WriteFile(_ context.Context, path string, data []byte) error {
	err := os.WriteFile(filepath.Join(v.Dir, path), data, 0o600)
	if err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}

	return nil
}

func (v *DirVolumeContext) ReadFile(_ context.Context, path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(v.Dir, path))
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}

	return data, nil
}

func (v *DirVolumeContext) OpenSandbox(_ context.Context, _, _ string) (Sandbox, error) {
	return nil, errors.New("OpenSandbox not supported without a driver")
}
