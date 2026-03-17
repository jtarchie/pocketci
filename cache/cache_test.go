package cache_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

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
		return nil, nil
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mockCacheStore) Persist(_ context.Context, key string, reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
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
		return nil, nil
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *trackingMockCacheStore) Persist(_ context.Context, key string, reader io.Reader) error {
	m.persistCalls++
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
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
	assert.Expect(err).NotTo(gomega.HaveOccurred())
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
