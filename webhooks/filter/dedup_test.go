package filter_test

import (
	"testing"

	"github.com/jtarchie/pocketci/webhooks/filter"
	. "github.com/onsi/gomega"
)

func TestDedupKeyHash(t *testing.T) {
	t.Parallel()

	t.Run("returns nil for empty expression", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		hash, err := filter.DedupKeyHash("", baseEnv())
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(hash).To(BeNil())
	})

	t.Run("returns nil when expression evaluates to empty string", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		env := baseEnv()
		env.Headers["X-Delivery-ID"] = ""
		hash, err := filter.DedupKeyHash(`headers["X-Delivery-ID"]`, env)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(hash).To(BeNil())
	})

	t.Run("returns 16-byte hash for valid expression", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		env := baseEnv()
		env.Headers["X-GitHub-Delivery"] = "abc-123"
		hash, err := filter.DedupKeyHash(`headers["X-GitHub-Delivery"]`, env)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(hash).To(HaveLen(16))
	})

	t.Run("same input produces same hash", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		env := baseEnv()
		env.Headers["X-GitHub-Delivery"] = "abc-123"
		hash1, err := filter.DedupKeyHash(`headers["X-GitHub-Delivery"]`, env)
		assert.Expect(err).NotTo(HaveOccurred())
		hash2, err := filter.DedupKeyHash(`headers["X-GitHub-Delivery"]`, env)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(hash1).To(Equal(hash2))
	})

	t.Run("different input produces different hash", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		env1 := baseEnv()
		env1.Headers["X-GitHub-Delivery"] = "abc-123"
		env2 := baseEnv()
		env2.Headers["X-GitHub-Delivery"] = "def-456"
		hash1, err := filter.DedupKeyHash(`headers["X-GitHub-Delivery"]`, env1)
		assert.Expect(err).NotTo(HaveOccurred())
		hash2, err := filter.DedupKeyHash(`headers["X-GitHub-Delivery"]`, env2)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(hash1).NotTo(Equal(hash2))
	})

	t.Run("returns error for invalid expression", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		hash, err := filter.DedupKeyHash(`nonexistent_var`, baseEnv())
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(hash).To(BeNil())
	})

	t.Run("works with payload expressions", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)
		env := baseEnv()
		env.Payload = map[string]any{"ref": "refs/heads/main", "after": "deadbeef"}
		hash, err := filter.DedupKeyHash(`payload["after"]`, env)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(hash).To(HaveLen(16))
	})
}
