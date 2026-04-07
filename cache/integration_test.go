package cache_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/cache"
	fscache "github.com/jtarchie/pocketci/cache/filesystem"
	s3cache "github.com/jtarchie/pocketci/cache/s3"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/digitalocean"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/fly"
	"github.com/jtarchie/pocketci/orchestra/hetzner"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/s3config"
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
			return native.New(context.Background(), native.Config{Namespace: ns}, logger)
		},
	})

	// docker requires docker command and supports caching
	_, dockerErr := exec.LookPath("docker")
	if dockerErr == nil {
		entries = append(entries, driverEntry{
			name: "docker",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return docker.New(context.Background(), docker.Config{Namespace: ns}, logger)
			},
		})
	}

	// digitalocean requires DIGITALOCEAN_TOKEN env var
	if token := os.Getenv("DIGITALOCEAN_TOKEN"); token != "" {
		entries = append(entries, driverEntry{
			name: "digitalocean",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return digitalocean.New(context.Background(), digitalocean.Config{ServerConfig: digitalocean.ServerConfig{Token: token}, Namespace: ns}, logger)
			},
		})
	}

	// hetzner requires HETZNER_TOKEN env var
	if token := os.Getenv("HETZNER_TOKEN"); token != "" {
		entries = append(entries, driverEntry{
			name: "hetzner",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return hetzner.New(context.Background(), hetzner.Config{ServerConfig: hetzner.ServerConfig{Token: token}, Namespace: ns}, logger)
			},
		})
	}

	// fly requires FLY_API_TOKEN env var
	if token := os.Getenv("FLY_API_TOKEN"); token != "" {
		entries = append(entries, driverEntry{
			name: "fly",
			factory: func(ns string, logger *slog.Logger) (orchestra.Driver, error) {
				return fly.New(context.Background(), fly.Config{ServerConfig: fly.ServerConfig{Token: token}, Namespace: ns}, logger)
			},
		})
	}

	return entries
}

func TestCacheIntegration(t *testing.T) {
	_, minioErr := exec.LookPath("minio")
	if minioErr != nil {
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

				store, err := s3cache.New(ctx, s3cache.Config{Config: s3config.Config{
					Bucket:          minio.Bucket(),
					Endpoint:        minio.Endpoint(),
					Region:          "us-east-1",
					AccessKeyID:     minio.AccessKeyID(),
					SecretAccessKey: minio.SecretAccessKey(),
					ForcePathStyle:  true,
					Prefix:          "integration-test",
				}})
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				namespace1 := "cache-test-1-" + gonanoid.Must()
				driver1, err := entry.factory(namespace1, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = driver1.Close() }()

				driver1 = cache.WrapWithCaching(driver1, store, "zstd", "integration-test", logger)

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

				driver2 = cache.WrapWithCaching(driver2, store, "zstd", "integration-test", logger)

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

				missStore, err := s3cache.New(ctx, s3cache.Config{Config: s3config.Config{
					Bucket:          minio.Bucket(),
					Endpoint:        minio.Endpoint(),
					Region:          "us-east-1",
					AccessKeyID:     minio.AccessKeyID(),
					SecretAccessKey: minio.SecretAccessKey(),
					ForcePathStyle:  true,
				}})
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				driver = cache.WrapWithCaching(driver, missStore, "zstd", "", logger)

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
	driver, err := native.New(context.Background(), native.Config{Namespace: namespace}, logger)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _ = driver.Close() }()

	wrappedDriver := cache.WrapWithCaching(driver, nil, "", "", logger)

	assert.Expect(wrappedDriver.Name()).To(gomega.Equal(driver.Name()))

	vol, err := wrappedDriver.CreateVolume(ctx, "test-vol", 0)
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _ = vol.Cleanup(ctx) }()

	assert.Expect(vol.Name()).To(gomega.Equal("test-vol"))
}

func TestCacheFilesystemIntegration(t *testing.T) {
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

			t.Run("filesystem cache persists volume data across runs", func(t *testing.T) {
				cacheDir := t.TempDir()
				volumeName := "cache-fs-test-vol"
				mountPath := "/cachevol"
				testData := "fs-cached-data-" + gonanoid.Must()

				store, err := fscache.New(fscache.Config{
					Directory: cacheDir,
				})
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				namespace1 := "fs-cache-test-1-" + gonanoid.Must()
				driver1, err := entry.factory(namespace1, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = driver1.Close() }()

				driver1 = cache.WrapWithCaching(driver1, store, "zstd", "fs-test", logger)

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

				err = container1.Cleanup(ctx)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				err = vol1.Cleanup(ctx)
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				err = driver1.Close()
				assert.Expect(err).NotTo(gomega.HaveOccurred())

				// Second run: restore from filesystem cache
				namespace2 := "fs-cache-test-2-" + gonanoid.Must()
				driver2, err := entry.factory(namespace2, logger)
				assert.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = driver2.Close() }()

				driver2 = cache.WrapWithCaching(driver2, store, "zstd", "fs-test", logger)

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
				}, "10s", "100ms").Should(gomega.BeTrue(), "cached data should be restored from filesystem")
			})
		})
	}
}
