package cache

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrCacheMiss is returned by Restore when the requested key does not exist in the cache.
var ErrCacheMiss = errors.New("cache miss")

// CacheStore defines the interface for cache storage backends (e.g., S3).
type CacheStore interface {
	// Restore downloads and returns a reader for the cached content.
	// Returns ErrCacheMiss if the cache key doesn't exist.
	Restore(ctx context.Context, key string) (io.ReadCloser, error)

	// Persist uploads content from the reader to the cache.
	Persist(ctx context.Context, key string, reader io.Reader) error

	// Exists checks if a cache key exists.
	Exists(ctx context.Context, key string) (bool, error)

	// Delete removes a cache entry.
	Delete(ctx context.Context, key string) error
}

// CacheOptions configures caching behavior.
type CacheOptions struct {
	// KeyPrefix is prepended to all cache keys (e.g., pipeline name).
	KeyPrefix string

	// TTL is the cache expiration duration. Zero means no expiration.
	TTL time.Duration

	// Compression specifies the compression algorithm (e.g., "zstd", "gzip", "none").
	Compression string

	// RestoreOnly skips persisting to cache on volume cleanup.
	// Useful for read-only caches shared from another source.
	RestoreOnly bool

	// PersistOnly skips restoring from cache on volume creation.
	// Useful for write-through scenarios.
	PersistOnly bool
}

// VolumeDataAccessor provides methods to copy data to/from a volume.
// Drivers that support this interface can participate in caching.
type VolumeDataAccessor interface {
	// CopyToVolume writes tar data to a volume.
	// The reader should provide a tar archive that will be extracted to the volume root.
	CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error

	// CopyFromVolume reads tar data from a volume.
	// Returns a tar archive of the volume contents.
	CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error)

	// ReadFilesFromVolume reads specific files/directories from a volume.
	// Returns a tar archive containing only the requested paths.
	// Directories are included recursively. Paths are relative to the volume root.
	ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error)
}
