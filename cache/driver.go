package cache

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

// CachingDriver wraps a Driver to provide transparent volume caching.
type CachingDriver struct {
	inner      orchestra.Driver
	store      CacheStore
	compressor Compressor
	keyPrefix  string
	logger     *slog.Logger
	volOpts    []CachingVolumeOption
}

// NewCachingDriver creates a new caching driver wrapper.
// If the inner driver doesn't implement VolumeDataAccessor, caching is disabled
// with a warning log.
func NewCachingDriver(
	inner orchestra.Driver,
	store CacheStore,
	compressor Compressor,
	keyPrefix string,
	logger *slog.Logger,
	volOpts ...CachingVolumeOption,
) *CachingDriver {
	// Check if driver supports volume data access
	if _, ok := inner.(VolumeDataAccessor); !ok {
		logger.Warn("driver.cache.disabled",
			"driver", inner.Name(),
		)
	}

	return &CachingDriver{
		inner:      inner,
		store:      store,
		compressor: compressor,
		keyPrefix:  keyPrefix,
		logger:     logger,
		volOpts:    volOpts,
	}
}

// CreateVolume implements orchestra.Driver.
// Creates the underlying volume, wraps it with caching, and eagerly restores from cache.
func (d *CachingDriver) CreateVolume(ctx context.Context, name string, size int) (orchestra.Volume, error) {
	// Create the underlying volume
	vol, err := d.inner.CreateVolume(ctx, name, size)
	if err != nil {
		return nil, err
	}

	// Check if driver supports volume data access
	accessor, ok := d.inner.(VolumeDataAccessor)
	if !ok {
		// Driver doesn't support caching, return unwrapped volume
		return vol, nil
	}

	// Build cache key with prefix
	cacheKey := name
	if d.keyPrefix != "" {
		cacheKey = d.keyPrefix + "/" + name
	}

	// Wrap with caching volume
	cachingVol := NewCachingVolume(
		vol,
		accessor,
		d.store,
		d.compressor,
		cacheKey,
		d.logger,
		d.volOpts...,
	)

	// Eagerly restore from cache
	if err := cachingVol.RestoreFromCache(ctx); err != nil {
		d.logger.Warn("volume.restore.failed",
			"volume", name,
			"error", err,
		)
		// Continue without cache - don't fail the operation
	}

	return cachingVol, nil
}

// Close implements orchestra.Driver.
func (d *CachingDriver) Close() error {
	return d.inner.Close()
}

// Name implements orchestra.Driver.
func (d *CachingDriver) Name() string {
	return d.inner.Name()
}

// RunContainer implements orchestra.Driver.
func (d *CachingDriver) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	return d.inner.RunContainer(ctx, task)
}

// GetContainer implements orchestra.Driver.
func (d *CachingDriver) GetContainer(ctx context.Context, containerID string) (orchestra.Container, error) {
	return d.inner.GetContainer(ctx, containerID)
}

// CopyToVolume implements VolumeDataAccessor by delegating to the inner driver.
// This allows the caching driver to participate in workdir pre-seeding.
func (d *CachingDriver) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	accessor, ok := d.inner.(VolumeDataAccessor)
	if !ok {
		return fmt.Errorf("inner driver %q does not support volume data access", d.inner.Name())
	}

	return accessor.CopyToVolume(ctx, volumeName, reader)
}

// CopyFromVolume implements VolumeDataAccessor by delegating to the inner driver.
func (d *CachingDriver) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	accessor, ok := d.inner.(VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("inner driver %q does not support volume data access", d.inner.Name())
	}

	return accessor.CopyFromVolume(ctx, volumeName)
}

// ReadFilesFromVolume implements VolumeDataAccessor by delegating to the inner driver.
func (d *CachingDriver) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	accessor, ok := d.inner.(VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("inner driver %q does not support volume data access", d.inner.Name())
	}

	return accessor.ReadFilesFromVolume(ctx, volumeName, filePaths...)
}

var _ orchestra.Driver = (*CachingDriver)(nil)
var _ VolumeDataAccessor = (*CachingDriver)(nil)
