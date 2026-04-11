package main_test

import (
	"path/filepath"
	"testing"

	"github.com/jtarchie/pocketci/testhelpers"
	. "github.com/onsi/gomega"
)

// TestCacheWithNoBackend verifies graceful degradation when a pipeline declares
// caches but the server has no cache backend (S3) configured.
//
// Expected behaviour:
//   - Intra-run cache sharing still works: volumes declared as caches are
//     mounted as regular (ephemeral) volumes, so tasks within the same run
//     can share data via them.
//   - Inter-run persistence is absent: each run starts with an empty cache
//     volume, but the pipeline must still succeed rather than error.
//   - No panics or unexpected errors from the driver or runtime.
func TestCacheWithNoBackend(t *testing.T) {
	assert := NewGomegaWithT(t)

	examplePath, err := filepath.Abs("both/caches.yml")
	assert.Expect(err).NotTo(HaveOccurred())

	for _, driver := range []string{"docker", "native"} {
		t.Run(driver, func(t *testing.T) {
			assert := NewGomegaWithT(t)

			// No CacheS3Bucket set — cache backend is nil (no-op).
			runner := testhelpers.Runner{
				Pipeline:          examplePath,
				Driver:            driver,
				StorageSQLitePath: ":memory:",
			}

			// First run: intra-run sharing must work (task writes, next task reads).
			err := runner.Run(nil)
			assert.Expect(err).NotTo(HaveOccurred(), "first run should succeed without a cache backend")

			// Second run: starts with an empty cache (no inter-run persistence).
			// The pipeline must still complete successfully, not fail because
			// previously-written cache data is absent.
			err = runner.Run(nil)
			assert.Expect(err).NotTo(HaveOccurred(), "second run should succeed even though cache was not persisted")
		})
	}
}
