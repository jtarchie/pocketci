package cache

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

// CachingVolume wraps a volume to provide transparent S3-backed caching.
type CachingVolume struct {
	inner      orchestra.Volume
	accessor   VolumeDataAccessor
	store      CacheStore
	compressor Compressor
	cacheKey   string
	logger     *slog.Logger
	restored   bool
}

// NewCachingVolume creates a new caching volume wrapper.
func NewCachingVolume(
	inner orchestra.Volume,
	accessor VolumeDataAccessor,
	store CacheStore,
	compressor Compressor,
	cacheKey string,
	logger *slog.Logger,
) *CachingVolume {
	return &CachingVolume{
		inner:      inner,
		accessor:   accessor,
		store:      store,
		compressor: compressor,
		cacheKey:   cacheKey + ".tar" + compressor.Extension(),
		logger:     logger,
	}
}

// RestoreFromCache attempts to restore volume contents from the cache.
// This should be called after volume creation and before container execution.
func (v *CachingVolume) RestoreFromCache(ctx context.Context) error {
	if v.restored {
		return nil
	}

	v.restored = true

	v.logger.Debug("volume.check",
		"volume", v.inner.Name(),
		"cache_key", v.cacheKey,
	)

	// Check if cache exists before attempting to restore
	exists, err := v.store.Exists(ctx, v.cacheKey)
	if err != nil {
		return fmt.Errorf("failed to check cache existence: %w", err)
	}

	if !exists {
		v.logger.Debug("volume.cache.miss", "volume", v.inner.Name())

		return nil // Cache miss, nothing to restore
	}

	// Get compressed data from cache store
	reader, err := v.store.Restore(ctx, v.cacheKey)
	if err != nil {
		return fmt.Errorf("failed to restore from cache: %w", err)
	}

	if reader == nil {
		v.logger.Debug("volume.cache.miss", "volume", v.inner.Name())

		return nil // Cache miss, nothing to restore
	}

	defer func() {
		_ = reader.Close()
	}()

	v.logger.Info("volume.restore",
		"volume", v.inner.Name(),
		"cache_key", v.cacheKey,
	)

	// Decompress the data
	decompressed, err := v.compressor.Decompress(reader)
	if err != nil {
		return fmt.Errorf("failed to decompress cache data: %w", err)
	}

	defer func() {
		_ = decompressed.Close()
	}()

	// Copy tar data to volume
	err = v.accessor.CopyToVolume(ctx, v.inner.Name(), decompressed)
	if err != nil {
		return fmt.Errorf("failed to copy data to volume: %w", err)
	}

	v.logger.Info("volume.restore.success", "volume", v.inner.Name())

	return nil
}

// PersistToCache saves volume contents to the cache.
// This should be called before volume cleanup.
func (v *CachingVolume) PersistToCache(ctx context.Context) error {
	v.logger.Info("volume.persist",
		"volume", v.inner.Name(),
		"cache_key", v.cacheKey,
	)

	// Get tar data from volume
	reader, err := v.accessor.CopyFromVolume(ctx, v.inner.Name())
	if err != nil {
		return fmt.Errorf("failed to copy data from volume: %w", err)
	}

	defer func() {
		_ = reader.Close()
	}()

	// Create a pipe for compression
	pipeReader, pipeWriter := newPipe()

	// Compress in a goroutine
	errChan := make(chan error, 1)

	go func() {
		defer func() {
			_ = pipeWriter.Close()
		}()

		compressedWriter, err := v.compressor.Compress(pipeWriter)
		if err != nil {
			errChan <- fmt.Errorf("failed to create compressor: %w", err)

			return
		}

		defer func() {
			_ = compressedWriter.Close()
		}()

		_, err = copyBuffer(compressedWriter, reader)
		errChan <- err
	}()

	// Upload compressed data to cache store
	err = v.store.Persist(ctx, v.cacheKey, pipeReader)
	if err != nil {
		return fmt.Errorf("failed to persist to cache: %w", err)
	}

	// Check for compression errors
	if compressErr := <-errChan; compressErr != nil {
		return fmt.Errorf("compression failed: %w", compressErr)
	}

	v.logger.Info("volume.persisted.success", "volume", v.inner.Name())

	return nil
}

// Cleanup implements orchestra.Volume.
// Persists to cache before cleaning up the underlying volume.
func (v *CachingVolume) Cleanup(ctx context.Context) error {
	// Persist to cache before cleanup
	if err := v.PersistToCache(ctx); err != nil {
		v.logger.Warn("volume.persist.failed",
			"volume", v.inner.Name(),
			"error", err,
		)
		// Continue with cleanup even if persist fails
	}

	return v.inner.Cleanup(ctx)
}

// Name implements orchestra.Volume.
func (v *CachingVolume) Name() string {
	return v.inner.Name()
}

// Path implements orchestra.Volume.
func (v *CachingVolume) Path() string {
	return v.inner.Path()
}

var _ orchestra.Volume = (*CachingVolume)(nil)
