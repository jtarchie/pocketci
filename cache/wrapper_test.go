package cache_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/onsi/gomega"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// keyCaptureStore records every key passed to Exists, allowing tests to assert
// which cache keys were generated without needing a real storage backend.
type keyCaptureStore struct {
	mu   sync.Mutex
	keys []string
}

func (k *keyCaptureStore) Restore(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, cache.ErrCacheMiss
}

func (k *keyCaptureStore) Persist(_ context.Context, _ string, r io.Reader) error {
	_, _ = io.ReadAll(r)
	return nil
}

func (k *keyCaptureStore) Exists(_ context.Context, key string) (bool, error) {
	k.mu.Lock()
	k.keys = append(k.keys, key)
	k.mu.Unlock()
	return false, nil
}

func (k *keyCaptureStore) Delete(_ context.Context, _ string) error {
	return nil
}

func (k *keyCaptureStore) capturedKeys() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, len(k.keys))
	copy(out, k.keys)
	return out
}

var _ cache.CacheStore = (*keyCaptureStore)(nil)

// mockCachingDriver is a minimal orchestra.Driver + cache.VolumeDataAccessor
// implementation used to test the caching wrapper without a real container runtime.
type mockCachingDriver struct{}

func (m *mockCachingDriver) CreateVolume(_ context.Context, name string, _ int) (orchestra.Volume, error) {
	return &mockVolume{name: name}, nil
}

func (m *mockCachingDriver) RunContainer(_ context.Context, _ orchestra.Task) (orchestra.Container, error) {
	return nil, nil
}

func (m *mockCachingDriver) GetContainer(_ context.Context, _ string) (orchestra.Container, error) {
	return nil, nil
}

func (m *mockCachingDriver) Close() error { return nil }

func (m *mockCachingDriver) Name() string { return "mock" }

func (m *mockCachingDriver) CopyToVolume(_ context.Context, _ string, r io.Reader) error {
	_, _ = io.ReadAll(r)
	return nil
}

func (m *mockCachingDriver) CopyFromVolume(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockCachingDriver) ReadFilesFromVolume(_ context.Context, _ string, _ ...string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

var _ orchestra.Driver = (*mockCachingDriver)(nil)
var _ cache.VolumeDataAccessor = (*mockCachingDriver)(nil)

// Note: mockVolume is defined in cache_test.go (same package cache_test)

func TestAugmentKeyPrefix(t *testing.T) {
	t.Parallel()

	logger := discardLogger()

	t.Run("different jobs produce different cache keys for same volume name", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)
		ctx := context.Background()

		inner := &mockCachingDriver{}
		store := &keyCaptureStore{}
		baseDriver := cache.WrapWithCaching(inner, store, "zstd", "global-prefix", logger)

		driverA := cache.AugmentKeyPrefix(baseDriver, "pipeline-abc/job-a")
		driverB := cache.AugmentKeyPrefix(baseDriver, "pipeline-abc/job-b")

		_, _ = driverA.CreateVolume(ctx, "cache-node-modules", 0)
		_, _ = driverB.CreateVolume(ctx, "cache-node-modules", 0)

		keys := store.capturedKeys()
		assert.Expect(keys).To(gomega.HaveLen(2))
		assert.Expect(keys[0]).NotTo(gomega.Equal(keys[1]))
		assert.Expect(keys[0]).To(gomega.ContainSubstring("job-a"))
		assert.Expect(keys[1]).To(gomega.ContainSubstring("job-b"))
	})

	t.Run("same job produces same cache key across runs", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)
		ctx := context.Background()

		inner := &mockCachingDriver{}
		store := &keyCaptureStore{}
		baseDriver := cache.WrapWithCaching(inner, store, "zstd", "global-prefix", logger)

		driverRun1 := cache.AugmentKeyPrefix(baseDriver, "pipeline-abc/job-build")
		driverRun2 := cache.AugmentKeyPrefix(baseDriver, "pipeline-abc/job-build")

		_, _ = driverRun1.CreateVolume(ctx, "cache-node-modules", 0)
		_, _ = driverRun2.CreateVolume(ctx, "cache-node-modules", 0)

		keys := store.capturedKeys()
		assert.Expect(keys).To(gomega.HaveLen(2))
		assert.Expect(keys[0]).To(gomega.Equal(keys[1]))
	})

	t.Run("non-CachingDriver is returned unchanged", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		inner := &mockCachingDriver{}
		result := cache.AugmentKeyPrefix(inner, "pipeline-abc/job-a")

		assert.Expect(result).To(gomega.BeIdenticalTo(inner))
	})

	t.Run("key structure is fully qualified", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)
		ctx := context.Background()

		inner := &mockCachingDriver{}
		store := &keyCaptureStore{}
		baseDriver := cache.WrapWithCaching(inner, store, "zstd", "my-project", logger)
		jobDriver := cache.AugmentKeyPrefix(baseDriver, "pipeline-xyz/job-deploy")

		_, _ = jobDriver.CreateVolume(ctx, "cache-node-modules", 0)

		keys := store.capturedKeys()
		assert.Expect(keys).To(gomega.HaveLen(1))
		assert.Expect(keys[0]).To(gomega.Equal("my-project/pipeline-xyz/job-deploy/cache-node-modules.tar.zst"))
	})

	t.Run("no global prefix still produces scoped key", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)
		ctx := context.Background()

		inner := &mockCachingDriver{}
		store := &keyCaptureStore{}
		baseDriver := cache.WrapWithCaching(inner, store, "zstd", "", logger)
		jobDriver := cache.AugmentKeyPrefix(baseDriver, "pipeline-xyz/job-deploy")

		_, _ = jobDriver.CreateVolume(ctx, "cache-node-modules", 0)

		keys := store.capturedKeys()
		assert.Expect(keys).To(gomega.HaveLen(1))
		assert.Expect(keys[0]).To(gomega.Equal("pipeline-xyz/job-deploy/cache-node-modules.tar.zst"))
	})
}
