package auth_test

import (
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/server/auth"
	. "github.com/onsi/gomega"
)

func TestShareToken_RoundTrip(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	token, err := auth.GenerateShareToken("run-abc123", "my-secret")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(token).NotTo(BeEmpty())

	claims, err := auth.ValidateShareToken(token, "my-secret")
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(claims.RunID).To(Equal("run-abc123"))
}

func TestShareToken_WrongSecret(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	token, err := auth.GenerateShareToken("run-abc123", "correct-secret")
	assert.Expect(err).NotTo(HaveOccurred())

	_, err = auth.ValidateShareToken(token, "wrong-secret")
	assert.Expect(err).To(HaveOccurred())
}

func TestShareToken_CrossRunForgery(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	// Token for run A is not valid for run B, even with same secret.
	tokenA, err := auth.GenerateShareToken("run-A", "secret")
	assert.Expect(err).NotTo(HaveOccurred())

	// Replace runID prefix with run-B, keeping the signature from run-A.
	parts := strings.SplitN(tokenA, ".", 2)
	forged := "run-B." + parts[1]

	_, err = auth.ValidateShareToken(forged, "secret")
	assert.Expect(err).To(HaveOccurred())
}

func TestShareToken_TamperedSignature(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	token, err := auth.GenerateShareToken("run-abc123", "secret")
	assert.Expect(err).NotTo(HaveOccurred())

	// Flip the last character of the signature.
	tampered := token[:len(token)-1] + "X"

	_, err = auth.ValidateShareToken(tampered, "secret")
	assert.Expect(err).To(HaveOccurred())
}

func TestShareToken_MalformedNoDot(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	_, err := auth.ValidateShareToken("nodothere", "secret")
	assert.Expect(err).To(HaveOccurred())
}

func TestShareToken_MalformedEmpty(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	_, err := auth.ValidateShareToken("", "secret")
	assert.Expect(err).To(HaveOccurred())
}

func TestShareToken_EmptyRunID(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	_, err := auth.GenerateShareToken("", "secret")
	assert.Expect(err).To(HaveOccurred())
}

func TestShareToken_InvalidHexSignature(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	_, err := auth.ValidateShareToken("run-abc123.not-valid-hex!!", "secret")
	assert.Expect(err).To(HaveOccurred())
}
