package posthog_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/observability/posthog"
	. "github.com/onsi/gomega"
)

func TestPosthogNewValidConfig(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := posthog.New(posthog.Config{APIKey: "phc_test123"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Name()).To(Equal("posthog"))

	defer func() { _ = p.Close() }()
}

func TestPosthogNewWithEndpoint(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := posthog.New(posthog.Config{
		APIKey:   "phc_test123",
		Endpoint: "https://us.i.posthog.com",
	}, logger)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Name()).To(Equal("posthog"))

	defer func() { _ = p.Close() }()
}

func TestPosthogNewMissingAPIKey(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := posthog.New(posthog.Config{}, logger)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("API key"))
}

func TestPosthogSlogHandler(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := posthog.New(posthog.Config{APIKey: "phc_test123"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = p.Close() }()

	handler := p.SlogHandler(slog.NewTextHandler(io.Discard, nil))
	assert.Expect(handler).NotTo(BeNil())

	wrapped := slog.New(handler)
	wrapped.Info("test message")
}
