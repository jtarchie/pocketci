package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
)

// CopyToVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the DigitalOcean droplet.
func (d *DigitalOcean) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	err := d.ensureDroplet(ctx, orchestra.ContainerLimits{})
	if err != nil {
		return fmt.Errorf("failed to ensure droplet: %w", err)
	}

	accessor, ok := d.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return errors.New("inner docker driver does not support caching")
	}

	err = accessor.CopyToVolume(ctx, volumeName, reader)
	if err != nil {
		return fmt.Errorf("copy to volume: %w", err)
	}

	return nil
}

// CopyFromVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the DigitalOcean droplet.
func (d *DigitalOcean) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	err := d.ensureDroplet(ctx, orchestra.ContainerLimits{})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure droplet: %w", err)
	}

	accessor, ok := d.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return nil, errors.New("inner docker driver does not support caching")
	}

	rc, err := accessor.CopyFromVolume(ctx, volumeName)
	if err != nil {
		return nil, fmt.Errorf("copy from volume: %w", err)
	}

	return rc, nil
}

// ReadFilesFromVolume implements cache.VolumeDataAccessor by delegating to the inner
// Docker driver that runs on the DigitalOcean droplet.
func (d *DigitalOcean) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	err := d.ensureDroplet(ctx, orchestra.ContainerLimits{})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure droplet: %w", err)
	}

	accessor, ok := d.dockerDriver.(cache.VolumeDataAccessor)
	if !ok {
		return nil, errors.New("inner docker driver does not support caching")
	}

	rc, err := accessor.ReadFilesFromVolume(ctx, volumeName, filePaths...)
	if err != nil {
		return nil, fmt.Errorf("read files from volume: %w", err)
	}

	return rc, nil
}

var _ cache.VolumeDataAccessor = (*DigitalOcean)(nil)
