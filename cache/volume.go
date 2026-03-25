package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"

	"github.com/jtarchie/pocketci/orchestra"
)

// CachingVolume wraps a volume to provide transparent S3-backed caching.
type CachingVolume struct {
	inner       orchestra.Volume
	accessor    VolumeDataAccessor
	store       CacheStore
	compressor  Compressor
	cacheKey    string
	logger      *slog.Logger
	restored    bool
	restoreOnly bool
	persistOnly bool
}

// NewCachingVolume creates a new caching volume wrapper.
func NewCachingVolume(
	inner orchestra.Volume,
	accessor VolumeDataAccessor,
	store CacheStore,
	compressor Compressor,
	cacheKey string,
	logger *slog.Logger,
	opts ...CachingVolumeOption,
) *CachingVolume {
	v := &CachingVolume{
		inner:      inner,
		accessor:   accessor,
		store:      store,
		compressor: compressor,
		cacheKey:   cacheKey + ".tar" + compressor.Extension(),
		logger:     logger,
	}

	for _, opt := range opts {
		opt(v)
	}

	return v
}

// CachingVolumeOption configures a CachingVolume.
type CachingVolumeOption func(*CachingVolume)

// WithRestoreOnly configures the volume to only restore from cache, never persist.
func WithRestoreOnly() CachingVolumeOption {
	return func(v *CachingVolume) {
		v.restoreOnly = true
	}
}

// WithPersistOnly configures the volume to only persist to cache, never restore.
func WithPersistOnly() CachingVolumeOption {
	return func(v *CachingVolume) {
		v.persistOnly = true
	}
}

// RestoreFromCache attempts to restore volume contents from the cache.
// This should be called after volume creation and before container execution.
func (v *CachingVolume) RestoreFromCache(ctx context.Context) error {
	if v.restored {
		return nil
	}

	v.restored = true

	if v.persistOnly {
		v.logger.Debug("volume.restore.skipped", "volume", v.inner.Name(), "reason", "persist-only mode")

		return nil
	}

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
// If the store implements HashAwareCacheStore, content hashing is used
// to skip redundant uploads when the volume content has not changed.
func (v *CachingVolume) PersistToCache(ctx context.Context) error {
	if v.restoreOnly {
		v.logger.Debug("volume.persist.skipped", "volume", v.inner.Name(), "reason", "restore-only mode")

		return nil
	}

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

	// Hash the compressed output to detect unchanged content
	hasher := sha256.New()

	// Compress in a goroutine
	errChan := make(chan error, 1)

	go func() {
		defer func() {
			_ = pipeWriter.Close()
		}()

		// Tee the compressed output through the hasher
		hashedWriter := io.MultiWriter(pipeWriter, hasher)

		compressedWriter, err := v.compressor.Compress(hashedWriter)
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

	// Check if store supports hash-aware persistence
	hashStore, isHashAware := v.store.(HashAwareCacheStore)

	if isHashAware {
		// Buffer the compressed data so we can compute the hash before uploading
		compressed, err := io.ReadAll(pipeReader)
		if err != nil {
			return fmt.Errorf("failed to read compressed data: %w", err)
		}

		if compressErr := <-errChan; compressErr != nil {
			return fmt.Errorf("compression failed: %w", compressErr)
		}

		newHash := hex.EncodeToString(hasher.Sum(nil))

		storedHash, err := hashStore.GetHash(ctx, v.cacheKey)
		if err != nil {
			v.logger.Warn("volume.persist.hash.check.failed",
				"volume", v.inner.Name(),
				"error", err,
			)
			// Fall through to upload
		} else if storedHash == newHash {
			v.logger.Info("volume.persist.skipped",
				"volume", v.inner.Name(),
				"reason", "content unchanged",
			)

			return nil
		}

		err = hashStore.PersistWithHash(ctx, v.cacheKey, bytes.NewReader(compressed), newHash)
		if err != nil {
			return fmt.Errorf("failed to persist to cache: %w", err)
		}
	} else {
		// Upload compressed data to cache store
		err = v.store.Persist(ctx, v.cacheKey, pipeReader)
		if err != nil {
			return fmt.Errorf("failed to persist to cache: %w", err)
		}

		// Check for compression errors
		if compressErr := <-errChan; compressErr != nil {
			return fmt.Errorf("compression failed: %w", compressErr)
		}
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
