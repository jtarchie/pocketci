package s3

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/s3config"
)

// Config configures an S3-backed cache store.
type Config struct {
	s3config.Config

	// PartSize is the size of each multipart upload part in bytes.
	// Defaults to 10MB if zero.
	PartSize int64

	// Concurrency is the number of parts to upload in parallel.
	// Defaults to 3 if zero.
	Concurrency int
}

const (
	defaultPartSize    = 10 * 1024 * 1024
	defaultConcurrency = 3
)

// S3Store implements CacheStore using AWS S3.
type S3Store struct {
	*s3config.Client
	ttl         time.Duration
	partSize    int64
	concurrency int
}

// New creates a new S3-backed cache store from the given Config.
func New(ctx context.Context, cfg Config) (*S3Store, error) {
	client, err := s3config.NewClient(ctx, &cfg.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %w", err)
	}

	partSize := cfg.PartSize
	if partSize <= 0 {
		partSize = defaultPartSize
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}

	return &S3Store{
		Client:      client,
		ttl:         cfg.TTL,
		partSize:    partSize,
		concurrency: concurrency,
	}, nil
}

var _ cache.CacheStore = (*S3Store)(nil)

// Restore downloads cached content from S3.
func (s *S3Store) Restore(ctx context.Context, key string) (io.ReadCloser, error) {
	fullKey := s.FullKey(key)

	result, err := s.GetStream(ctx, fullKey)
	if err != nil {
		if s3config.IsNotFound(err) {
			return nil, cache.ErrCacheMiss
		}

		return nil, fmt.Errorf("failed to get object from S3: %w", err)
	}

	// Check if object has expired based on TTL
	if s.ttl > 0 && result.LastModified != nil {
		if time.Since(*result.LastModified) > s.ttl {
			_ = result.Body.Close()
			// Object expired, delete it and return cache miss
			_ = s.Delete(ctx, key)

			return nil, cache.ErrCacheMiss
		}
	}

	return result.Body, nil
}

// Persist uploads content to S3 using streaming multipart upload.
// Data is uploaded in chunks without buffering the entire content in memory.
func (s *S3Store) Persist(ctx context.Context, key string, reader io.Reader) error {
	fullKey := s.FullKey(key)

	err := s.PutStream(ctx, fullKey, reader, func(u *transfermanager.Options) {
		u.PartSizeBytes = s.partSize
		u.Concurrency = s.concurrency
	})
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	return nil
}

// Exists checks if a cache key exists in S3.
func (s *S3Store) Exists(ctx context.Context, key string) (bool, error) {
	fullKey := s.FullKey(key)

	result, err := s.HeadKey(ctx, fullKey)
	if err != nil {
		if s3config.IsNotFound(err) {
			return false, nil
		}

		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	// Check TTL expiration
	if s.ttl > 0 && result.LastModified != nil {
		if time.Since(*result.LastModified) > s.ttl {
			return false, nil
		}
	}

	return true, nil
}

// Delete removes a cache entry from S3.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	fullKey := s.FullKey(key)

	err := s.DeleteKey(ctx, fullKey)
	if err != nil {
		return fmt.Errorf("failed to delete object from S3: %w", err)
	}

	return nil
}
