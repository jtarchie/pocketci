package filesystem_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/cache/filesystem"
	"github.com/onsi/gomega"
)

func TestFilesystemStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("persist and restore round-trip", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		store, err := filesystem.New(filesystem.Config{
			Directory: t.TempDir(),
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = store.Persist(ctx, "test-key.tar.zst", bytes.NewReader([]byte("cached data")))
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		reader, err := store.Restore(ctx, "test-key.tar.zst")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(reader).NotTo(gomega.BeNil())

		data, err := io.ReadAll(reader)
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(reader.Close()).To(gomega.Succeed())
		assert.Expect(string(data)).To(gomega.Equal("cached data"))
	})

	t.Run("cache miss returns nil", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		store, err := filesystem.New(filesystem.Config{
			Directory: t.TempDir(),
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		reader, err := store.Restore(ctx, "missing-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(reader).To(gomega.BeNil())
	})

	t.Run("exists returns false on miss", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		store, err := filesystem.New(filesystem.Config{
			Directory: t.TempDir(),
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		exists, err := store.Exists(ctx, "missing-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(exists).To(gomega.BeFalse())
	})

	t.Run("exists returns true after persist", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		store, err := filesystem.New(filesystem.Config{
			Directory: t.TempDir(),
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = store.Persist(ctx, "test-key", bytes.NewReader([]byte("data")))
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		exists, err := store.Exists(ctx, "test-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(exists).To(gomega.BeTrue())
	})

	t.Run("delete removes entry", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		store, err := filesystem.New(filesystem.Config{
			Directory: t.TempDir(),
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = store.Persist(ctx, "test-key", bytes.NewReader([]byte("data")))
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = store.Delete(ctx, "test-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		exists, err := store.Exists(ctx, "test-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(exists).To(gomega.BeFalse())
	})

	t.Run("delete missing key is not an error", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		store, err := filesystem.New(filesystem.Config{
			Directory: t.TempDir(),
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = store.Delete(ctx, "missing-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
	})

	t.Run("TTL expiry returns cache miss", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)
		dir := t.TempDir()

		store, err := filesystem.New(filesystem.Config{
			Directory: dir,
			TTL:       1 * time.Millisecond,
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = store.Persist(ctx, "expiring-key", bytes.NewReader([]byte("data")))
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		// Set the file mtime to the past
		path := filepath.Join(dir, "expiring-key")
		past := time.Now().Add(-1 * time.Hour)
		assert.Expect(os.Chtimes(path, past, past)).To(gomega.Succeed())

		// Exists should return false
		exists, err := store.Exists(ctx, "expiring-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(exists).To(gomega.BeFalse())

		// Restore should return nil
		reader, err := store.Restore(ctx, "expiring-key")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(reader).To(gomega.BeNil())
	})

	t.Run("subdirectory key paths", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		store, err := filesystem.New(filesystem.Config{
			Directory: t.TempDir(),
		})
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		err = store.Persist(ctx, "prefix/sub/key.tar.zst", bytes.NewReader([]byte("nested data")))
		assert.Expect(err).NotTo(gomega.HaveOccurred())

		reader, err := store.Restore(ctx, "prefix/sub/key.tar.zst")
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(reader).NotTo(gomega.BeNil())

		data, err := io.ReadAll(reader)
		assert.Expect(err).NotTo(gomega.HaveOccurred())
		assert.Expect(reader.Close()).To(gomega.Succeed())
		assert.Expect(string(data)).To(gomega.Equal("nested data"))
	})

	t.Run("empty directory config returns error", func(t *testing.T) {
		t.Parallel()

		assert := gomega.NewGomegaWithT(t)

		_, err := filesystem.New(filesystem.Config{})
		assert.Expect(err).To(gomega.HaveOccurred())
	})
}
