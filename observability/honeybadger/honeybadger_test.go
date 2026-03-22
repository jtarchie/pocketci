package honeybadger_test

import (
	"log/slog"
	"testing"

	hb "github.com/honeybadger-io/honeybadger-go"
	"github.com/jtarchie/pocketci/observability"
	"github.com/jtarchie/pocketci/observability/honeybadger"
	. "github.com/onsi/gomega"
)

func TestHoneybadgerNewValidConfig(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)

	p, err := honeybadger.New(honeybadger.Config{APIKey: "hbp_test123"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Name()).To(Equal("honeybadger"))

	defer func() { _ = p.Close() }()
}

func TestHoneybadgerNewWithEnv(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)

	p, err := honeybadger.New(honeybadger.Config{APIKey: "hbp_test123", Env: "staging"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Name()).To(Equal("honeybadger"))

	defer func() { _ = p.Close() }()
}

func TestHoneybadgerNewMissingAPIKey(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)

	_, err := honeybadger.New(honeybadger.Config{}, logger)
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("API key"))
}

func TestHoneybadgerSlogHandler(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	logger := slog.New(slog.DiscardHandler)

	p, err := honeybadger.New(honeybadger.Config{APIKey: "hbp_test123"}, logger)
	assert.Expect(err).NotTo(HaveOccurred())

	defer func() { _ = p.Close() }()

	handler := p.SlogHandler(slog.DiscardHandler)
	assert.Expect(handler).NotTo(BeNil())

	_, isTee := handler.(*observability.TeeHandler)
	assert.Expect(isTee).To(BeTrue(), "honeybadger should return a TeeHandler")

	wrapped := slog.New(handler)
	wrapped.Info("test message")
}

func TestHoneybadgerEventWithTestBackend(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	backend := &hb.TestBackend{}

	client := hb.New(hb.Configuration{
		APIKey:  "test-key",
		Backend: backend,
	})

	err := client.Event("pipeline.started", map[string]any{
		"pipeline": "my-pipeline",
	})
	assert.Expect(err).NotTo(HaveOccurred())

	client.Flush()

	events := backend.GetEvents()
	assert.Expect(events).To(HaveLen(1))
	assert.Expect(events[0].EventType).To(Equal("pipeline.started"))
}
