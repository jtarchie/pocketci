package server_test

import (
	"testing"

	"github.com/jtarchie/pocketci/server"
	. "github.com/onsi/gomega"
)

func TestParseAllowedFeatures(t *testing.T) {
	t.Parallel()

	t.Run("wildcard returns all features", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		features, err := server.ParseAllowedFeatures("*")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(features).To(ConsistOf(server.FeatureWebhooks, server.FeatureSecrets, server.FeatureNotifications, server.FeatureFetch, server.FeatureResume))
	})

	t.Run("empty returns all features", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		features, err := server.ParseAllowedFeatures("")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(features).To(ConsistOf(server.FeatureWebhooks, server.FeatureSecrets, server.FeatureNotifications, server.FeatureFetch, server.FeatureResume))
	})

	t.Run("single feature", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		features, err := server.ParseAllowedFeatures("webhooks")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(features).To(Equal([]server.Feature{server.FeatureWebhooks}))
	})

	t.Run("multiple features", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		features, err := server.ParseAllowedFeatures("webhooks,secrets")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(features).To(ConsistOf(server.FeatureWebhooks, server.FeatureSecrets))
	})

	t.Run("trims whitespace", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		features, err := server.ParseAllowedFeatures(" webhooks , secrets ")
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(features).To(ConsistOf(server.FeatureWebhooks, server.FeatureSecrets))
	})

	t.Run("unknown feature returns error", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		_, err := server.ParseAllowedFeatures("webhooks,bogus")
		assert.Expect(err).To(HaveOccurred())
		assert.Expect(err.Error()).To(ContainSubstring("unknown feature"))
		assert.Expect(err.Error()).To(ContainSubstring("bogus"))
	})
}

func TestIsFeatureEnabled(t *testing.T) {
	t.Parallel()

	t.Run("returns true when feature is in allowed list", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		allowed := []server.Feature{server.FeatureWebhooks, server.FeatureSecrets}
		assert.Expect(server.IsFeatureEnabled(server.FeatureWebhooks, allowed)).To(BeTrue())
		assert.Expect(server.IsFeatureEnabled(server.FeatureSecrets, allowed)).To(BeTrue())
	})

	t.Run("returns false when feature is not in allowed list", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		allowed := []server.Feature{server.FeatureWebhooks}
		assert.Expect(server.IsFeatureEnabled(server.FeatureSecrets, allowed)).To(BeFalse())
		assert.Expect(server.IsFeatureEnabled(server.FeatureNotifications, allowed)).To(BeFalse())
	})

	t.Run("returns false for empty allowed list", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		assert.Expect(server.IsFeatureEnabled(server.FeatureWebhooks, []server.Feature{})).To(BeFalse())
	})
}
