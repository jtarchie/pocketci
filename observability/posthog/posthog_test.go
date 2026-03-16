package posthog_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/observability"
	_ "github.com/jtarchie/pocketci/observability/posthog"
	. "github.com/onsi/gomega"
)

func TestPosthogRegistered(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	found := false

	observability.Each(func(name string, _ observability.InitFunc) {
		if name == "posthog" {
			found = true
		}
	})

	assert.Expect(found).To(BeTrue(), "posthog should be registered")
}

func TestPosthogNewValidDSN(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := observability.GetFromDSN("posthog://phc_test123", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Name()).To(Equal("posthog"))

	defer p.Close()
}

func TestPosthogNewWithEndpoint(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := observability.GetFromDSN("posthog://phc_test123?endpoint=https://us.i.posthog.com", logger)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Name()).To(Equal("posthog"))

	defer p.Close()
}

func TestPosthogNewMissingAPIKey(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := observability.GetFromDSN("posthog://", logger)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("API key"))
}

func TestPosthogSlogHandler(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := observability.GetFromDSN("posthog://phc_test123", logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer p.Close()

	handler := p.SlogHandler(slog.NewTextHandler(io.Discard, nil))
	assert.Expect(handler).NotTo(BeNil())

	wrapped := slog.New(handler)
	wrapped.Info("test message")
}
