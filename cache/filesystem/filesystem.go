package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jtarchie/pocketci/cache"
)

// Config configures a filesystem-backed cache store.
type Config struct {
	Directory string
	TTL       time.Duration
}

// FilesystemStore implements CacheStore using the local filesystem.
type FilesystemStore struct {
	directory string
	ttl       time.Duration
}

// New creates a new filesystem-backed cache store.
func New(cfg Config) (*FilesystemStore, error) {
	if cfg.Directory == "" {
		return nil, errors.New("cache directory must be specified")
	}

	if err := os.MkdirAll(cfg.Directory, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &FilesystemStore{
		directory: cfg.Directory,
		ttl:       cfg.TTL,
	}, nil
}

var _ cache.CacheStore = (*FilesystemStore)(nil)
var _ cache.HashAwareCacheStore = (*FilesystemStore)(nil)

func (s *FilesystemStore) fullPath(key string) string {
	return filepath.Join(s.directory, key)
}

// Restore downloads cached content from the filesystem.
func (s *FilesystemStore) Restore(_ context.Context, key string) (io.ReadCloser, error) {
	path := s.fullPath(key)

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to stat cache file: %w", err)
	}

	if s.ttl > 0 && time.Since(info.ModTime()) > s.ttl {
		_ = os.Remove(path)

		return nil, nil
	}

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to open cache file: %w", err)
	}

	return file, nil
}

// Persist writes content to the filesystem.
func (s *FilesystemStore) Persist(_ context.Context, key string, reader io.Reader) error {
	path := s.fullPath(key)

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("failed to create cache subdirectory: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create cache file: %w", err)
	}

	defer func() {
		_ = file.Close()
	}()

	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// Exists checks if a cache key exists on the filesystem.
func (s *FilesystemStore) Exists(_ context.Context, key string) (bool, error) {
	path := s.fullPath(key)

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("failed to stat cache file: %w", err)
	}

	if s.ttl > 0 && time.Since(info.ModTime()) > s.ttl {
		return false, nil
	}

	return true, nil
}

// Delete removes a cache entry from the filesystem.
func (s *FilesystemStore) Delete(_ context.Context, key string) error {
	path := s.fullPath(key)

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to delete cache file: %w", err)
	}

	return nil
}

// GetHash returns the stored content hash for a cache key.
// The hash is stored in a sidecar file at <key>.hash.
func (s *FilesystemStore) GetHash(_ context.Context, key string) (string, error) {
	hashPath := s.fullPath(key) + ".hash"

	data, err := os.ReadFile(hashPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}

		return "", fmt.Errorf("failed to read hash file: %w", err)
	}

	return string(data), nil
}

// PersistWithHash writes content to the filesystem and stores the content hash in a sidecar file.
func (s *FilesystemStore) PersistWithHash(ctx context.Context, key string, reader io.Reader, hash string) error {
	if err := s.Persist(ctx, key, reader); err != nil {
		return err
	}

	hashPath := s.fullPath(key) + ".hash"

	if err := os.WriteFile(hashPath, []byte(hash), 0o640); err != nil {
		return fmt.Errorf("failed to write hash file: %w", err)
	}

	return nil
}
