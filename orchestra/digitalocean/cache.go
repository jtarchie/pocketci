package digitalocean

import (
	"context"
	"fmt"
	"io"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
)

// CopyToVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the DigitalOcean droplet.
func (d *DigitalOcean) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	if err := d.ensureDroplet(ctx, orchestra.ContainerLimits{}); err != nil {
		return fmt.Errorf("failed to ensure droplet: %w", err)
	}

	accessor, ok := d.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return fmt.Errorf("inner docker driver does not support caching")
	}

	return accessor.CopyToVolume(ctx, volumeName, reader)
}

// CopyFromVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the DigitalOcean droplet.
func (d *DigitalOcean) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	if err := d.ensureDroplet(ctx, orchestra.ContainerLimits{}); err != nil {
		return nil, fmt.Errorf("failed to ensure droplet: %w", err)
	}

	accessor, ok := d.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("inner docker driver does not support caching")
	}

	return accessor.CopyFromVolume(ctx, volumeName)
}

// ReadFilesFromVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the DigitalOcean droplet.
func (d *DigitalOcean) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	if err := d.ensureDroplet(ctx, orchestra.ContainerLimits{}); err != nil {
		return nil, fmt.Errorf("failed to ensure droplet: %w", err)
	}

	accessor, ok := d.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("inner docker driver does not support caching")
	}

	return accessor.ReadFilesFromVolume(ctx, volumeName, filePaths...)
}

var _ cache.VolumeDataAccessor = (*DigitalOcean)(nil)
