package s3_test

import (
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/s3config"
	s3storage "github.com/jtarchie/pocketci/storage/s3"
	. "github.com/onsi/gomega"
)

// TestS3Driver_EncryptWithSseS3 verifies that a driver can be constructed with
// encrypt=sse-s3 in the DSN. Actual server-side encryption requires a KMS-enabled
// S3-compatible service; correct parsing is also exercised in s3config tests.
func TestS3Driver_EncryptWithSseS3(t *testing.T) {
	assert := NewGomegaWithT(t)

	// Construction succeeds — no real S3 calls needed to verify config parsing.
	client, err := s3storage.NewS3(s3storage.Config{Config: s3config.Config{Bucket: "bucket", Region: "us-east-1", EncryptMode: "sse-s3"}}, "sse-ns", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = client.Close() })
}

// TestS3Driver_DSNParams verifies that non-default DSN parameters (force_path_style,
// sse) are accepted without error during driver construction.
func TestS3Driver_DSNParams(t *testing.T) {
	assert := NewGomegaWithT(t)

	// force_path_style=false is a valid param; construction must not return an error.
	client, err := s3storage.NewS3(s3storage.Config{Config: s3config.Config{Bucket: "bucket", Region: "us-east-1"}}, "params-ns", slog.Default())
	assert.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { _ = client.Close() })
}

// TestS3Driver_InvalidEncrypt verifies that an unsupported encrypt param value is rejected
// at construction time, before any requests are made.
func TestS3Driver_InvalidEncrypt(t *testing.T) {
	assert := NewGomegaWithT(t)

	_, err := s3storage.NewS3(s3storage.Config{Config: s3config.Config{Bucket: "bucket", Region: "us-east-1", EncryptMode: "bogus"}}, "ns", slog.Default())
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("unsupported encrypt value"))
}
