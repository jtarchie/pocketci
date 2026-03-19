package server

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestRedactSecretValues(t *testing.T) {
	t.Parallel()

	t.Run("replaces known secret", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := redactSecretValues("<div>my-super-secret in output</div>", []string{"my-super-secret"})
		assert.Expect(result).To(ContainSubstring("[REDACTED]"))
		assert.Expect(result).NotTo(ContainSubstring("my-super-secret"))
	})

	t.Run("skips empty values", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		html := "<div>some content</div>"
		result := redactSecretValues(html, []string{"", "valid-secret"})
		assert.Expect(result).To(Equal(html))
	})

	t.Run("replaces multiple secrets", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := redactSecretValues("<div>token-A and token-B</div>", []string{"token-A", "token-B"})
		assert.Expect(result).NotTo(ContainSubstring("token-A"))
		assert.Expect(result).NotTo(ContainSubstring("token-B"))
		assert.Expect(result).To(ContainSubstring("[REDACTED]"))
	})

	t.Run("handles nil slice", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		html := "<div>no secrets here</div>"
		assert.Expect(redactSecretValues(html, nil)).To(Equal(html))
	})
}
