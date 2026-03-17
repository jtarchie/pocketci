package hetzner

import (
	"context"
	"fmt"
	"io"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
)

// CopyToVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the Hetzner server.
func (h *Hetzner) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	if err := h.ensureServer(ctx, orchestra.ContainerLimits{}); err != nil {
		return fmt.Errorf("failed to ensure server: %w", err)
	}

	accessor, ok := h.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return fmt.Errorf("inner docker driver does not support caching")
	}

	return accessor.CopyToVolume(ctx, volumeName, reader)
}

// CopyFromVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the Hetzner server.
func (h *Hetzner) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	if err := h.ensureServer(ctx, orchestra.ContainerLimits{}); err != nil {
		return nil, fmt.Errorf("failed to ensure server: %w", err)
	}

	accessor, ok := h.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("inner docker driver does not support caching")
	}

	return accessor.CopyFromVolume(ctx, volumeName)
}

// ReadFilesFromVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the Hetzner server.
func (h *Hetzner) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	if err := h.ensureServer(ctx, orchestra.ContainerLimits{}); err != nil {
		return nil, fmt.Errorf("failed to ensure server: %w", err)
	}

	accessor, ok := h.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("inner docker driver does not support caching")
	}

	return accessor.ReadFilesFromVolume(ctx, volumeName, filePaths...)
}

var _ cache.VolumeDataAccessor = (*Hetzner)(nil)
