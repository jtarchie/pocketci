package testhelpers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phayes/freeport"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/onsi/gomega"
)

type MinioServer struct {
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	dataDir  string
	endpoint string
	bucket   string
}

// StartMinIO starts a MinIO server for testing and returns a server handle.
// The server will use a random free port and a temporary data directory.
// Call Stop() to clean up when done.
func StartMinIO(t *testing.T) *MinioServer {
	t.Helper()

	assert := gomega.NewGomegaWithT(t)

	dataDir, err := os.MkdirTemp("", "minio-test-*")
	assert.Expect(err).NotTo(gomega.HaveOccurred())

	// Get a free port from the OS to avoid conflicts
	port, err := freeport.GetFreePort()
	assert.Expect(err).NotTo(gomega.HaveOccurred())
	endpoint := fmt.Sprintf("http://localhost:%d", port)

	// S3 bucket names must be lowercase alphanumeric or hyphens, cannot start/end with hyphens.
	id := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(gonanoid.Must()), "_", ""), "-", "")
	bucket := "testcache" + id

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "minio", "server", dataDir, "--address", fmt.Sprintf(":%d", port), "--quiet")
	cmd.Env = append(os.Environ(),
		"MINIO_ROOT_USER=minioadmin",
		"MINIO_ROOT_PASSWORD=minioadmin",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	assert.Expect(err).NotTo(gomega.HaveOccurred())

	server := &MinioServer{
		cmd:      cmd,
		cancel:   cancel,
		dataDir:  dataDir,
		endpoint: endpoint,
		bucket:   bucket,
	}

	// Wait for MinIO to be ready and create the bucket
	assert.Eventually(func() bool {
		bucketPath := dataDir + "/" + bucket
		if err := os.MkdirAll(bucketPath, 0755); err != nil {
			return false
		}
		return true
	}, "10s", "100ms").Should(gomega.BeTrue(), "MinIO should start")

	time.Sleep(500 * time.Millisecond)

	// Set AWS credentials for S3 client
	t.Setenv("AWS_ACCESS_KEY_ID", "minioadmin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "minioadmin")

	return server
}

// Stop stops the MinIO server and cleans up temporary files.
func (m *MinioServer) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_ = m.cmd.Wait()
	}

	if m.dataDir != "" {
		_ = os.RemoveAll(m.dataDir)
	}
}

// CacheURL returns the S3 URL for use with the cache parameter.
func (m *MinioServer) CacheURL() string {
	// m.endpoint is "http://localhost:PORT"; insert credentials after the scheme.
	endpointWithCreds := strings.Replace(m.endpoint, "://", "://minioadmin:minioadmin@", 1)
	return fmt.Sprintf("s3://%s/%s?region=us-east-1", endpointWithCreds, m.bucket)
}

// Endpoint returns the HTTP endpoint of the MinIO server.
func (m *MinioServer) Endpoint() string {
	return m.endpoint
}

// Bucket returns the name of the test bucket.
func (m *MinioServer) Bucket() string {
	return m.bucket
}

// AccessKeyID returns the MinIO root access key ID.
func (m *MinioServer) AccessKeyID() string {
	return "minioadmin"
}

// SecretAccessKey returns the MinIO root secret access key.
func (m *MinioServer) SecretAccessKey() string {
	return "minioadmin"
}
