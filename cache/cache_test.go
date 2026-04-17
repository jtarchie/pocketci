package cache_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/onsi/gomega"
)

type mockVolume struct {
	name string
}

func (m *mockVolume) Cleanup(ctx context.Context) error {
	return nil
}

func (m *mockVolume) Name() string {
	return m.name
}

func (m *mockVolume) Path() string {
	return "/mock/" + m.name
}

type mockAccessor struct{}

func (m *mockAccessor) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	// Mock implementation: just consume the reader
	_, _ = io.ReadAll(reader)
	return nil
}

func (m *mockAccessor) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("mock tar data"))), nil
}

func (m *mockAccessor) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("mock tar data"))), nil
}

var _ orchestra.Volume = (*mockVolume)(nil)
var _ cache.VolumeDataAccessor = (*mockAccessor)(nil)

func TestCompressor(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)

	t.Run("zstd compressor", func(t *testing.T) {
		t.Parallel()

		compressor := cache.NewZstdCompressor(0)

		original := []byte("hello world, this is some test data that should compress")

		var compressed bytes.Buffer

		writer, err := compressor.Compress(&compressed)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		_, err = writer.Write(original)
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(writer.Close()).To(gomega.Succeed())

		reader, err := compressor.Decompress(&compressed)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		decompressed, err := io.ReadAll(reader)
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(reader.Close()).To(gomega.Succeed())

		assert.Expect(decompressed).To(gomega.Equal(original))
		assert.Expect(compressor.Extension()).To(gomega.Equal(".zst"))
	})

	t.Run("no compressor", func(t *testing.T) {
		t.Parallel()

		compressor := cache.NewCompressor("none")

		original := []byte("hello world")

		var buf bytes.Buffer

		writer, err := compressor.Compress(&buf)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		_, err = writer.Write(original)
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(writer.Close()).To(gomega.Succeed())

		assert.Expect(buf.Bytes()).To(gomega.Equal(original))
		assert.Expect(compressor.Extension()).To(gomega.Equal(""))
	})
}

type mockCacheStore struct {
	data map[string][]byte
}

func newMockCacheStore() *mockCacheStore {
	return &mockCacheStore{data: make(map[string][]byte)}
}

func (m *mockCacheStore) Restore(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.data[key]
	if !ok {
		return nil, cache.ErrCacheMiss
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockCacheStore) Persist(_ context.Context, key string, reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read all: %w", err)
	}

	m.data[key] = data

	return nil
}

func (m *mockCacheStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data[key]

	return ok, nil
}

func (m *mockCacheStore) Delete(_ context.Context, key string) error {
	delete(m.data, key)

	return nil
}

type trackingMockCacheStore struct {
	data         map[string][]byte
	restoreCalls int
	existsCalls  int
	persistCalls int
}

func newTrackingMockCacheStore() *trackingMockCacheStore {
	return &trackingMockCacheStore{data: make(map[string][]byte)}
}

func (m *trackingMockCacheStore) Restore(_ context.Context, key string) (io.ReadCloser, error) {
	m.restoreCalls++
	data, ok := m.data[key]
	if !ok {
		return nil, cache.ErrCacheMiss
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *trackingMockCacheStore) Persist(_ context.Context, key string, reader io.Reader) error {
	m.persistCalls++
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read all: %w", err)
	}

	m.data[key] = data

	return nil
}

func (m *trackingMockCacheStore) Exists(_ context.Context, key string) (bool, error) {
	m.existsCalls++
	_, ok := m.data[key]

	return ok, nil
}

func (m *trackingMockCacheStore) Delete(_ context.Context, key string) error {
	delete(m.data, key)

	return nil
}

func TestMockCacheStore(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)
	ctx := context.Background()

	store := newMockCacheStore()

	reader, err := store.Restore(ctx, "missing")
	assert.Expect(err).To(gomega.MatchError(cache.ErrCacheMiss))
	assert.Expect(reader).To(gomega.BeNil())

	err = store.Persist(ctx, "test-key", bytes.NewReader([]byte("test data")))
	assert.Expect(err).NotTo(gomega.HaveOccurred())

	exists, err := store.Exists(ctx, "test-key")
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(exists).To(gomega.BeTrue())

	reader, err = store.Restore(ctx, "test-key")
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(reader).NotTo(gomega.BeNil())

	data, err := io.ReadAll(reader)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(string(data)).To(gomega.Equal("test data"))

	err = store.Delete(ctx, "test-key")
	assert.Expect(err).NotTo(gomega.HaveOccurred())

	exists, err = store.Exists(ctx, "test-key")
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(exists).To(gomega.BeFalse())
}

func TestCachingVolumeExistsCheckBeforeRestore(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)
	ctx := context.Background()

	t.Run("cache miss: Exists is called but Restore is not", func(t *testing.T) {
		t.Parallel()

		store := newTrackingMockCacheStore()
		compressor := cache.NewCompressor("none")
		mockVolume := &mockVolume{name: "test-vol"}
		logger := slog.Default()

		vol := cache.NewCachingVolume(
			mockVolume,
			&mockAccessor{},
			store,
			compressor,
			"test-key",
			logger,
		)

		err := vol.RestoreFromCache(ctx)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify that Exists was called
		assert.Expect(store.existsCalls).To(gomega.Equal(1))

		// Verify that Restore was NOT called (optimization: skip download on cache miss)
		assert.Expect(store.restoreCalls).To(gomega.Equal(0))
	})

	t.Run("cache hit: Exists and Restore are both called", func(t *testing.T) {
		t.Parallel()

		store := newTrackingMockCacheStore()
		compressor := cache.NewCompressor("none")
		mockVolume := &mockVolume{name: "test-vol"}
		logger := slog.Default()

		// Persist some data first
		err := store.Persist(ctx, "test-key.tar", bytes.NewReader([]byte("test data")))
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		vol := cache.NewCachingVolume(
			mockVolume,
			&mockAccessor{},
			store,
			compressor,
			"test-key",
			logger,
		)

		err = vol.RestoreFromCache(ctx)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify that Exists was called
		assert.Expect(store.existsCalls).To(gomega.Equal(1))

		// Verify that Restore was called (cache hit)
		assert.Expect(store.restoreCalls).To(gomega.Equal(1))
	})

	t.Run("RestoreFromCache is idempotent", func(t *testing.T) {
		t.Parallel()

		store := newTrackingMockCacheStore()
		compressor := cache.NewCompressor("none")
		mockVolume := &mockVolume{name: "test-vol"}
		logger := slog.Default()

		vol := cache.NewCachingVolume(
			mockVolume,
			&mockAccessor{},
			store,
			compressor,
			"test-key",
			logger,
		)

		// Call RestoreFromCache multiple times
		err := vol.RestoreFromCache(ctx)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = vol.RestoreFromCache(ctx)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = vol.RestoreFromCache(ctx)
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		// Verify that Exists was called only once (due to idempotency)
		assert.Expect(store.existsCalls).To(gomega.Equal(1))

		// Verify that Restore was called only once (or not at all)
		assert.Expect(store.restoreCalls).To(gomega.Equal(0))
	})
}

func TestCachingVolumeRestoreOnlyMode(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)
	ctx := context.Background()

	store := newTrackingMockCacheStore()
	compressor := cache.NewCompressor("none")
	logger := slog.Default()

	// Pre-populate cache
	err := store.Persist(ctx, "test-key.tar", bytes.NewReader([]byte("cached data")))
	assert.Expect(err).NotTo(gomega.HaveOccurred())

	vol := cache.NewCachingVolume(
		&mockVolume{name: "test-vol"},
		&mockAccessor{},
		store,
		compressor,
		"test-key",
		logger,
		cache.WithRestoreOnly(),
	)

	// Restore should work
	err = vol.RestoreFromCache(ctx)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(store.existsCalls).To(gomega.Equal(1))
	assert.Expect(store.restoreCalls).To(gomega.Equal(1))

	// Cleanup (which calls PersistToCache) should NOT persist
	store.persistCalls = 0
	err = vol.Cleanup(ctx)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(store.persistCalls).To(gomega.Equal(0))
}

// failingPersistStore returns an error from Persist without reading the whole
// stream. This simulates a mid-upload S3 failure and exercises the pipe
// cleanup in CachingVolume.PersistToCache.
type failingPersistStore struct {
	err       error
	readBytes int
}

func (s *failingPersistStore) Restore(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, cache.ErrCacheMiss
}

func (s *failingPersistStore) Persist(_ context.Context, _ string, reader io.Reader) error {
	// Drain `readBytes` and then return without further reads, emulating
	// an HTTP upload that errors partway through.
	if s.readBytes > 0 {
		_, _ = io.CopyN(io.Discard, reader, int64(s.readBytes))
	}

	return s.err
}

func (s *failingPersistStore) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (s *failingPersistStore) Delete(_ context.Context, _ string) error {
	return nil
}

type largeVolumeAccessor struct{}

func (a *largeVolumeAccessor) CopyToVolume(_ context.Context, _ string, reader io.Reader) error {
	_, _ = io.ReadAll(reader)
	return nil
}

func (a *largeVolumeAccessor) CopyFromVolume(_ context.Context, _ string) (io.ReadCloser, error) {
	// Return enough bytes that io.Copy makes multiple passes after the
	// consumer stops reading — this is what would hang the goroutine
	// without the CloseWithError in persistDirect. Use high-entropy data
	// so compression can't collapse the whole stream into one tiny write.
	buf := make([]byte, 16*1024*1024)
	for i := range buf {
		buf[i] = byte(i)
	}

	return io.NopCloser(bytes.NewReader(buf)), nil
}

func (a *largeVolumeAccessor) ReadFilesFromVolume(_ context.Context, _ string, _ ...string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

var _ cache.VolumeDataAccessor = (*largeVolumeAccessor)(nil)

func TestCachingVolumePersistReleasesGoroutineOnStoreError(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)
	ctx := context.Background()

	store := &failingPersistStore{err: errors.New("s3 upload failed"), readBytes: 1024}
	compressor := cache.NewCompressor("none")
	logger := slog.Default()

	vol := cache.NewCachingVolume(
		&mockVolume{name: "test-vol"},
		&largeVolumeAccessor{},
		store,
		compressor,
		"test-key",
		logger,
	)

	done := make(chan error, 1)
	go func() {
		done <- vol.PersistToCache(ctx)
	}()

	select {
	case err := <-done:
		assert.Expect(err).To(gomega.HaveOccurred())
		assert.Expect(err.Error()).To(gomega.ContainSubstring("s3 upload failed"))
	case <-time.After(5 * time.Second):
		t.Fatal("PersistToCache did not return after store.Persist error — pipe/goroutine leak")
	}
}

func TestCachingVolumePersistOnlyMode(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)
	ctx := context.Background()

	store := newTrackingMockCacheStore()
	compressor := cache.NewCompressor("none")
	logger := slog.Default()

	vol := cache.NewCachingVolume(
		&mockVolume{name: "test-vol"},
		&mockAccessor{},
		store,
		compressor,
		"test-key",
		logger,
		cache.WithPersistOnly(),
	)

	// Restore should be skipped (persist-only mode)
	err := vol.RestoreFromCache(ctx)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(store.existsCalls).To(gomega.Equal(0))
	assert.Expect(store.restoreCalls).To(gomega.Equal(0))

	// Persist should work
	err = vol.PersistToCache(ctx)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	assert.Expect(store.persistCalls).To(gomega.Equal(1))
}
