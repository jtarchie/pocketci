package backwards_test

import (
	"context"
	"testing"

	backwards "github.com/jtarchie/pocketci/runtime/backwards"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	. "github.com/onsi/gomega"
)

func TestResourceStoreSaveAndGetLatest(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/my-resource"
	version := map[string]string{"ref": "abc123"}

	err = backwards.SaveResourceVersion(ctx, store, name, version, "build-job")
	assert.Expect(err).NotTo(HaveOccurred())

	latest, err := backwards.GetLatestResourceVersion(ctx, store, name)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(latest).NotTo(BeNil())
	assert.Expect(latest.Version).To(Equal(version))
	assert.Expect(latest.JobName).To(Equal("build-job"))
	assert.Expect(latest.FetchedAt).NotTo(BeEmpty())
}

func TestResourceStoreSaveMultipleVersions(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/counter"

	for i := range 3 {
		v := map[string]string{"version": string(rune('1' + i))}
		err := backwards.SaveResourceVersion(ctx, store, name, v, "job")
		assert.Expect(err).NotTo(HaveOccurred())
	}

	versions, err := backwards.ListResourceVersions(ctx, store, name, 0)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(versions).To(HaveLen(3))

	assert.Expect(versions[0].Version["version"]).To(Equal("1"))
	assert.Expect(versions[2].Version["version"]).To(Equal("3"))

	latest, err := backwards.GetLatestResourceVersion(ctx, store, name)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(latest).NotTo(BeNil())
	assert.Expect(latest.Version["version"]).To(Equal("3"))
}

func TestResourceStoreDeduplication(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/my-resource"
	version := map[string]string{"ref": "abc123"}

	err = backwards.SaveResourceVersion(ctx, store, name, version, "job-a")
	assert.Expect(err).NotTo(HaveOccurred())

	// Save the same version again with a different job name.
	err = backwards.SaveResourceVersion(ctx, store, name, version, "job-b")
	assert.Expect(err).NotTo(HaveOccurred())

	// Count should still be 1.
	versions, err := backwards.ListResourceVersions(ctx, store, name, 0)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(versions).To(HaveLen(1))

	// Mutable fields should be updated.
	assert.Expect(versions[0].JobName).To(Equal("job-b"))
}

func TestResourceStoreGetVersionsAfter(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/counter"

	for i := range 5 {
		v := map[string]string{"version": string(rune('a' + i))}
		err := backwards.SaveResourceVersion(ctx, store, name, v, "job")
		assert.Expect(err).NotTo(HaveOccurred())
	}

	// Get versions after index 2 (version "c").
	after := map[string]string{"version": "c"}
	results, err := backwards.GetVersionsAfter(ctx, store, name, after)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(results).To(HaveLen(2))
	assert.Expect(results[0].Version["version"]).To(Equal("d"))
	assert.Expect(results[1].Version["version"]).To(Equal("e"))
}

func TestResourceStoreGetVersionsAfterNil(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/counter"

	for i := range 3 {
		v := map[string]string{"version": string(rune('a' + i))}
		err := backwards.SaveResourceVersion(ctx, store, name, v, "job")
		assert.Expect(err).NotTo(HaveOccurred())
	}

	// nil afterVersion returns all versions.
	results, err := backwards.GetVersionsAfter(ctx, store, name, nil)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(results).To(HaveLen(3))
}

func TestResourceStoreGetVersionsAfterNotFound(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/counter"

	for i := range 3 {
		v := map[string]string{"version": string(rune('a' + i))}
		err := backwards.SaveResourceVersion(ctx, store, name, v, "job")
		assert.Expect(err).NotTo(HaveOccurred())
	}

	// Unknown afterVersion returns all versions.
	unknown := map[string]string{"version": "zzz"}
	results, err := backwards.GetVersionsAfter(ctx, store, name, unknown)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(results).To(HaveLen(3))
}

func TestResourceStoreListWithLimit(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/counter"

	for i := range 5 {
		v := map[string]string{"version": string(rune('a' + i))}
		err := backwards.SaveResourceVersion(ctx, store, name, v, "job")
		assert.Expect(err).NotTo(HaveOccurred())
	}

	// Limit to 3.
	versions, err := backwards.ListResourceVersions(ctx, store, name, 3)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(versions).To(HaveLen(3))
	assert.Expect(versions[0].Version["version"]).To(Equal("a"))
	assert.Expect(versions[2].Version["version"]).To(Equal("c"))
}

func TestResourceStoreEmpty(t *testing.T) {
	assert := NewGomegaWithT(t)
	logger := discardLogger()

	store, err := storagesqlite.NewSqlite(storagesqlite.Config{Path: ":memory:"}, "test-rv", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	name := "pipeline1/nonexistent"

	latest, err := backwards.GetLatestResourceVersion(ctx, store, name)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(latest).To(BeNil())

	versions, err := backwards.ListResourceVersions(ctx, store, name, 0)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(versions).To(BeEmpty())

	results, err := backwards.GetVersionsAfter(ctx, store, name, nil)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(results).To(BeEmpty())
}
