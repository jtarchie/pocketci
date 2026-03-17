package cache_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/cache"
	_ "github.com/jtarchie/pocketci/orchestra/cache/s3"
	"github.com/jtarchie/pocketci/orchestra/digitalocean"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/fly"
	"github.com/jtarchie/pocketci/orchestra/hetzner"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/testhelpers"
	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/onsi/gomega"
)

type driverFactory func(namespace string, logger *slog.Logger) (orchestra.Driver, error)

type driverEntry struct {
	name    string
	factory driverFactory
}

// getAvailableDrivers returns a list of drivers available based on environment
// and system requirements. Only includes drivers that support caching.
func getAvailableDrivers() []driverEntry {
	var entries []driverEntry

	// native is always available and supports caching
	entries = append(entries, driverEntry{
		name: "native",
		factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
			return native.New(native.Config{Namespace: ns}, logger)
		},
	})

	// docker requires docker command and supports caching
	if _, err := exec.LookPath("docker"); err == nil {
		entries = append(entries, driverEntry{
			name: "docker",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return docker.New(docker.Config{Namespace: ns}, logger)
			},
		})
	}

	// digitalocean requires DIGITALOCEAN_TOKEN env var
	if token := os.Getenv("DIGITALOCEAN_TOKEN"); token != "" {
		entries = append(entries, driverEntry{
			name: "digitalocean",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return digitalocean.New(digitalocean.Config{Namespace: ns, Token: token}, logger)
			},
		})
	}

	// hetzner requires HETZNER_TOKEN env var
	if token := os.Getenv("HETZNER_TOKEN"); token != "" {
		entries = append(entries, driverEntry{
			name: "hetzner",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return hetzner.New(hetzner.Config{Namespace: ns, Token: token}, logger)
			},
		})
	}

	// fly requires FLY_API_TOKEN env var
	if token := os.Getenv("FLY_API_TOKEN"); token != "" {
		entries = append(entries, driverEntry{
			name: "fly",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return fly.New(fly.Config{Namespace: ns, Token: token}, logger)
			},
		})
	}

	return entries
}

func TestCacheIntegration(t *testing.T) {
	if _, err := exec.LookPath("minio"); err != nil {
		t.Skip("minio not installed, skipping integration test")
	}

	entries := getAvailableDrivers()
	if len(entries) == 0 {
		t.Skip("no drivers available for testing")
	}

	for _, entry := range entries {
		entry := entry
		t.Run(entry.name, func(t *testing.T) {
			assert := gomega.NewGomegaWithT(t)
			ctx := context.Background()
			logger := slog.Default()

			minio := testhelpers.StartMinIO(t)
			defer minio.Stop()

			t.Run("cache persists volume data across runs", func(t *testing.T) {
				volumeName := "cache-test-vol"
				mountPath := "/cachevol"
				testData := "cached-data-" + gonanoid.Must()

				namespace1 := "cache-test-1-" + gonanoid.Must()
				driver1, err := entry.factory(namespace1, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = driver1.Close() }()

				cacheParams := map[string]string{
					"cache":             minio.CacheURL(),
					"cache_compression": "zstd",
					"cache_prefix":      "integration-test",
				}
				driver1, err = cache.WrapWithCaching(driver1, cacheParams, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				vol1, err := driver1.CreateVolume(ctx, volumeName, 0)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				taskID1 := gonanoid.Must()
				container1, err := driver1.RunContainer(ctx, orchestra.Task{
					ID:      taskID1,
					Image:   "busybox",
					Command: []string{"sh", "-c", fmt.Sprintf("echo '%s' > .%s/data.txt", testData, mountPath)},
					Mounts: orchestra.Mounts{
						{Name: volumeName, Path: mountPath},
					},
				})
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				assert.Eventually(func() bool {
					status, err := container1.Status(ctx)
					if err != nil {
						return false
					}
					return status.IsDone() && status.ExitCode() == 0
				}, "30s", "100ms").Should(gomega.BeTrue(), "container should complete successfully")

				// Cleanup container before volume
				err = container1.Cleanup(ctx)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				err = vol1.Cleanup(ctx)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				err = driver1.Close()
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				namespace2 := "cache-test-2-" + gonanoid.Must()
				driver2, err := entry.factory(namespace2, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = driver2.Close() }()

				driver2, err = cache.WrapWithCaching(driver2, cacheParams, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				vol2, err := driver2.CreateVolume(ctx, volumeName, 0)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = vol2.Cleanup(ctx) }()

				taskID2 := gonanoid.Must()
				container2, err := driver2.RunContainer(ctx, orchestra.Task{
					ID:      taskID2,
					Image:   "busybox",
					Command: []string{"cat", "." + mountPath + "/data.txt"},
					Mounts: orchestra.Mounts{
						{Name: volumeName, Path: mountPath},
					},
				})
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				assert.Eventually(func() bool {
					status, err := container2.Status(ctx)
					if err != nil {
						return false
					}
					return status.IsDone() && status.ExitCode() == 0
				}, "30s", "100ms").Should(gomega.BeTrue(), "container should complete successfully")

				assert.Eventually(func() bool {
					stdout := &strings.Builder{}
					stderr := &strings.Builder{}
					_ = container2.Logs(ctx, stdout, stderr, false)
					return strings.Contains(stdout.String(), testData)
				}, "10s", "100ms").Should(gomega.BeTrue(), "cached data should be restored")
			})

			t.Run("cache miss on first run", func(t *testing.T) {
				volumeName := "fresh-vol-" + gonanoid.Must()
				mountPath := "/freshvol"

				namespace := "cache-miss-" + gonanoid.Must()
				driver, err := entry.factory(namespace, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = driver.Close() }()

				cacheParams := map[string]string{
					"cache":             minio.CacheURL(),
					"cache_compression": "zstd",
				}
				driver, err = cache.WrapWithCaching(driver, cacheParams, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				vol, err := driver.CreateVolume(ctx, volumeName, 0)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = vol.Cleanup(ctx) }()

				assert.Expect(vol.Name()).To(gomega.Equal(volumeName))

				taskID := gonanoid.Must()
				container, err := driver.RunContainer(ctx, orchestra.Task{
					ID:      taskID,
					Image:   "busybox",
					Command: []string{"sh", "-c", fmt.Sprintf("echo 'new data' > .%s/test.txt && cat .%s/test.txt", mountPath, mountPath)},
					Mounts: orchestra.Mounts{
						{Name: volumeName, Path: mountPath},
					},
				})
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				assert.Eventually(func() bool {
					status, err := container.Status(ctx)
					if err != nil {
						return false
					}
					return status.IsDone() && status.ExitCode() == 0
				}, "30s", "100ms").Should(gomega.BeTrue())

				assert.Eventually(func() bool {
					stdout := &strings.Builder{}
					stderr := &strings.Builder{}
					_ = container.Logs(ctx, stdout, stderr, false)
					return strings.Contains(stdout.String(), "new data")
				}, "10s", "100ms").Should(gomega.BeTrue())
			})
		})
	}
}

func TestCacheWithoutCachingEnabled(t *testing.T) {
	t.Parallel()

	assert := gomega.NewGomegaWithT(t)
	ctx := context.Background()
	logger := slog.Default()

	namespace := "no-cache-" + gonanoid.Must()
	driver, err := native.New(native.Config{Namespace: namespace}, logger)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _ = driver.Close() }()

	emptyParams := map[string]string{}
	wrappedDriver, err := cache.WrapWithCaching(driver, emptyParams, logger)
	assert.Expect(err).NotTo(gomega.HaveOccurred())

	assert.Expect(wrappedDriver.Name()).To(gomega.Equal(driver.Name()))

	vol, err := wrappedDriver.CreateVolume(ctx, "test-vol", 0)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _ = vol.Cleanup(ctx) }()

	assert.Expect(vol.Name()).To(gomega.Equal("test-vol"))
}
