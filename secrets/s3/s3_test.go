package s3_test

import (
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/s3config"
	"github.com/jtarchie/pocketci/secrets/s3"
	. "github.com/onsi/gomega"
)

func TestS3Secrets_RequiresKey(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)

	t.Run("missing key returns error", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		cfg := s3config.Config{
			Bucket:   "test-bucket",
			Region:   "us-east-1",
			Endpoint: "https://s3.amazonaws.com",
		}
		_, err := s3.New(s3.Config{Config: cfg}, logger)
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("Key"))
	})

	t.Run("no encrypt mode is allowed (app-layer AES only)", func(t *testing.T) {
		t.Parallel()

		assert := NewGomegaWithT(t)

		cfg := s3config.Config{
			Bucket:   "test-bucket",
			Region:   "us-east-1",
			Endpoint: "https://s3.amazonaws.com",
			Key:      "passphrase",
		}
		_, err := s3.New(s3.Config{Config: cfg}, logger)
		if err != nil {
			assert.Expect(err.Error()).NotTo(ContainSubstring("sse="))
			assert.Expect(err.Error()).NotTo(ContainSubstring("requires sse"))
		}
	})
}
