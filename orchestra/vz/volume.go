//go:build darwin && cgo

package vz

import (
	"context"
	"fmt"
	"os"
)

// Volume represents a shared directory between host and Apple VZ guest via virtiofs.
type Volume struct {
	name     string
	hostPath string
}

// Name returns the volume name.
func (v *Volume) Name() string {
	return v.name
}

// Path returns the guest-side mount path.
func (v *Volume) Path() string {
	return "/mnt/volumes/" + v.name
}

// Cleanup removes the host-side volume directory.
func (v *Volume) Cleanup(_ context.Context) error {
	err := os.RemoveAll(v.hostPath)
	if err != nil {
		return fmt.Errorf("failed to remove volume directory: %w", err)
	}

	return nil
}
