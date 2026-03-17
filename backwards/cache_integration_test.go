package backwards_test

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/klauspost/compress/zstd"
	gonanoid "github.com/matoous/go-nanoid/v2"

	_ "github.com/jtarchie/pocketci/orchestra/cache/s3"
	"github.com/jtarchie/pocketci/testhelpers"
	. "github.com/onsi/gomega"
)

// TestCacheS3Persistence tests that caches are persisted to S3 and restored
// across completely separate pipeline runs.
func TestCacheS3Persistence(t *testing.T) {
	// Skip if minio is not available
	if _, err := exec.LookPath("minio"); err != nil {
		t.Skip("minio not installed, skipping S3 cache integration test")
	}

	// Start MinIO server
	minio := testhelpers.StartMinIO(t)
	defer minio.Stop()

	cacheURL := minio.CacheURL()

	// Create a unique cache value so we know it came from S3
	cacheValue := gonanoid.Must()

	t.Run("docker", func(t *testing.T) {
		testCachePersistence(t, minio, "docker", cacheURL, cacheValue)
	})

	t.Run("native", func(t *testing.T) {
		testCachePersistence(t, minio, "native", cacheURL, cacheValue+"-native")
	})
}

func testCachePersistence(t *testing.T, minio *testhelpers.MinioServer, driverDSN, cacheURL, cacheValue string) {
	assert := NewGomegaWithT(t)

	// Create temp directory for pipeline files
	tmpDir := t.TempDir()

	// Pipeline 1: Write to cache
	writePipeline := fmt.Sprintf(`---
jobs:
  - name: write-job
    plan:
      - task: write-cache
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: busybox }
          caches:
            - path: mycache
          run:
            path: sh
            args:
              - -c
              - |
                  echo "Writing cache value: %s"
                  echo "%s" > ./mycache/value.txt
                  cat ./mycache/value.txt
        assert:
          stdout: "%s"
          code: 0
`, cacheValue, cacheValue, cacheValue)

	writePipelinePath := tmpDir + "/write-pipeline.yml"
	err := os.WriteFile(writePipelinePath, []byte(writePipeline), 0644)
	assert.Expect(err).NotTo(HaveOccurred())

	// Pipeline 2: Read from cache (should be restored from S3)
	readPipeline := fmt.Sprintf(`---
jobs:
  - name: read-job
    plan:
      - task: read-cache
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: busybox }
          caches:
            - path: mycache
          run:
            path: sh
            args:
              - -c
              - |
                  echo "Reading cache value:"
                  cat ./mycache/value.txt
        assert:
          stdout: "%s"
          code: 0
`, cacheValue)

	readPipelinePath := tmpDir + "/read-pipeline.yml"
	err = os.WriteFile(readPipelinePath, []byte(readPipeline), 0644)
	assert.Expect(err).NotTo(HaveOccurred())

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Run pipeline 1: Write to cache
	t.Log("Running write pipeline...")
	runner1 := testhelpers.Runner{
		Pipeline:          writePipelinePath,
		Driver:            driverDSN,
		StorageSQLitePath: ":memory:",
		CacheURL:          cacheURL,
		CacheCompression:  "zstd",
		CachePrefix:       "test",
	}
	err = runner1.Run(logger)
	assert.Expect(err).NotTo(HaveOccurred(), "Write pipeline should succeed")

	// Verify cache was persisted to S3
	t.Log("Verifying cache exists in MinIO...")
	s3Client := createMinIOClient(t, minio.Endpoint(), "us-east-1")
	cacheKey := "test/cache-mycache.tar.zst" // prefix/cache-{path}.tar.zst
	verifyObjectExists(t, s3Client, minio.Bucket(), cacheKey)

	// Run pipeline 2: Read from cache (completely new runner instance)
	// This tests that the cache was persisted to S3 and restored
	t.Log("Running read pipeline (should restore from S3)...")
	runner2 := testhelpers.Runner{
		Pipeline:          readPipelinePath,
		Driver:            driverDSN,
		StorageSQLitePath: ":memory:",
		CacheURL:          cacheURL,
		CacheCompression:  "zstd",
		CachePrefix:       "test",
	}
	err = runner2.Run(logger)
	assert.Expect(err).NotTo(HaveOccurred(), "Read pipeline should succeed - cache should be restored from S3")
}

// TestCacheS3Contents tests that cached files are correctly stored in S3
// by downloading, decompressing, and verifying the actual file contents.
func TestCacheS3Contents(t *testing.T) {
	// Skip if minio is not available
	if _, err := exec.LookPath("minio"); err != nil {
		t.Skip("minio not installed, skipping S3 cache content verification test")
	}

	// Start MinIO server
	minio := testhelpers.StartMinIO(t)
	defer minio.Stop()

	cacheURL := minio.CacheURL()

	// Create a unique cache value
	cacheValue := gonanoid.Must()

	t.Run("docker", func(t *testing.T) {
		testCacheContents(t, minio, "docker", cacheURL, cacheValue)
	})

	t.Run("native", func(t *testing.T) {
		testCacheContents(t, minio, "native", cacheURL, cacheValue+"-native")
	})
}

func testCacheContents(t *testing.T, minio *testhelpers.MinioServer, driverDSN, cacheURL, cacheValue string) {
	assert := NewGomegaWithT(t)

	// Create temp directory for pipeline file
	tmpDir := t.TempDir()

	// Pipeline: Write to cache
	writePipeline := fmt.Sprintf(`---
jobs:
  - name: write-job
    plan:
      - task: write-cache
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: { repository: busybox }
          caches:
            - path: mycache
          run:
            path: sh
            args:
              - -c
              - |
                  echo "Writing cache value: %s"
                  echo "%s" > ./mycache/value.txt
                  cat ./mycache/value.txt
        assert:
          stdout: "%s"
          code: 0
`, cacheValue, cacheValue, cacheValue)

	writePipelinePath := tmpDir + "/write-pipeline.yml"
	err := os.WriteFile(writePipelinePath, []byte(writePipeline), 0644)
	assert.Expect(err).NotTo(HaveOccurred())

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Run pipeline: Write to cache
	t.Log("Running write pipeline...")
	runner := testhelpers.Runner{
		Pipeline:          writePipelinePath,
		Driver:            driverDSN,
		StorageSQLitePath: ":memory:",
		CacheURL:          cacheURL,
		CacheCompression:  "zstd",
		CachePrefix:       "test",
	}
	err = runner.Run(logger)
	assert.Expect(err).NotTo(HaveOccurred(), "Write pipeline should succeed")

	// Download and verify cache contents
	t.Log("Downloading and verifying cache contents from MinIO...")
	s3Client := createMinIOClient(t, minio.Endpoint(), "us-east-1")
	cacheKey := "test/cache-mycache.tar.zst" // prefix/cache-{path}.tar.zst
	downloadAndVerifyCache(t, s3Client, minio.Bucket(), cacheKey, "value.txt", cacheValue)
}

// createMinIOClient creates an S3 client configured for MinIO.
func createMinIOClient(t *testing.T, endpoint, region string) *s3.Client {
	t.Helper()

	assert := NewGomegaWithT(t)

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	assert.Expect(err).NotTo(HaveOccurred())

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // Required for MinIO
		if region != "" {
			o.Region = region
		}
	})

	return client
}

// verifyObjectExists checks if an S3 object exists and has non-zero size.
func verifyObjectExists(t *testing.T, client *s3.Client, bucket, key string) {
	t.Helper()

	assert := NewGomegaWithT(t)

	ctx := context.Background()
	result, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	assert.Expect(err).NotTo(HaveOccurred(), "S3 object should exist at key: "+key)
	assert.Expect(result.ContentLength).NotTo(Equal(aws.Int64(0)), "S3 object should have non-zero size")

	t.Logf("Verified S3 object exists: s3://%s/%s (size: %d bytes)", bucket, key, *result.ContentLength)
}

// downloadAndVerifyCache downloads a cache object from S3, decompresses it,
// extracts the tar archive, and verifies the expected file content.
func downloadAndVerifyCache(t *testing.T, client *s3.Client, bucket, key, expectedFilePath, expectedContent string) {
	t.Helper()

	assert := NewGomegaWithT(t)
	ctx := context.Background()

	// Download object from S3
	t.Logf("Downloading cache from s3://%s/%s", bucket, key)
	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	assert.Expect(err).NotTo(HaveOccurred(), "Should download object from S3")
	defer func() { _ = result.Body.Close() }()

	// Decompress with zstd
	t.Log("Decompressing zstd stream...")
	decoder, err := zstd.NewReader(result.Body)
	assert.Expect(err).NotTo(HaveOccurred(), "Should create zstd decoder")
	defer decoder.Close()

	// Extract tar archive
	t.Log("Extracting tar archive...")
	tarReader := tar.NewReader(decoder)

	found := false
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		assert.Expect(err).NotTo(HaveOccurred(), "Should read tar header")

		t.Logf("Found tar entry: %s (size: %d)", header.Name, header.Size)

		// Check if this is the file we're looking for
		if strings.Contains(header.Name, expectedFilePath) {
			content, err := io.ReadAll(tarReader)
			assert.Expect(err).NotTo(HaveOccurred(), "Should read file content from tar")

			contentStr := strings.TrimSpace(string(content))
			t.Logf("File content: %q", contentStr)

			assert.Expect(contentStr).To(Equal(expectedContent), "Cache file content should match expected value")
			found = true

			break
		}
	}

	assert.Expect(found).To(BeTrue(), "Should find expected file in cache: "+expectedFilePath)
	t.Logf("Successfully verified cache contents!")
}
