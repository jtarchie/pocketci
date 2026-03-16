package observability_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/observability"
	. "github.com/onsi/gomega"
)

func TestRegisterAndGetFromDSN(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	observability.Register("testprovider", func(dsn string, logger *slog.Logger) (observability.Provider, error) {
		return &fakeProvider{name: "testprovider"}, nil
	})

	p, err := observability.GetFromDSN("testprovider://mykey", slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(p.Name()).To(Equal("testprovider"))
}

func TestGetFromDSNUnknownProvider(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	_, err := observability.GetFromDSN("unknown://key", slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.Expect(err).To(HaveOccurred())
	assert.Expect(err.Error()).To(ContainSubstring("unknown observability provider"))
}

func TestGetFromDSNInvalidDSN(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	_, err := observability.GetFromDSN("://", slog.New(slog.NewTextHandler(io.Discard, nil)))
	assert.Expect(err).To(HaveOccurred())
}

func TestTeeHandler(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	primary := &recordingHandler{}
	secondary := &recordingHandler{}

	tee := observability.NewTeeHandler(primary, secondary)

	logger := slog.New(tee)
	logger.Info("hello")

	assert.Expect(primary.records).To(HaveLen(1))
	assert.Expect(secondary.records).To(HaveLen(1))
	assert.Expect(primary.records[0].Message).To(Equal("hello"))
	assert.Expect(secondary.records[0].Message).To(Equal("hello"))
}

func TestTeeHandlerWithAttrs(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	primary := &recordingHandler{}
	secondary := &recordingHandler{}

	tee := observability.NewTeeHandler(primary, secondary)
	teeWithAttrs := tee.WithAttrs([]slog.Attr{slog.String("key", "value")})

	logger := slog.New(teeWithAttrs)
	logger.Info("test")

	assert.Expect(primary.records).To(HaveLen(1))
	assert.Expect(secondary.records).To(HaveLen(1))
}

func TestTeeHandlerWithGroup(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	primary := &recordingHandler{}
	secondary := &recordingHandler{}

	tee := observability.NewTeeHandler(primary, secondary)
	teeWithGroup := tee.WithGroup("mygroup")

	logger := slog.New(teeWithGroup)
	logger.Info("grouped")

	assert.Expect(primary.records).To(HaveLen(1))
	assert.Expect(secondary.records).To(HaveLen(1))
}

// fakeProvider is a minimal Provider implementation for registry tests.
type fakeProvider struct {
	name string
}

func (f *fakeProvider) Name() string                                    { return f.name }
func (f *fakeProvider) Event(_ string, _ map[string]any) error          { return nil }
func (f *fakeProvider) SlogHandler(next slog.Handler) slog.Handler      { return next }
func (f *fakeProvider) Close() error                                    { return nil }

// recordingHandler records slog records for test assertions.
type recordingHandler struct {
	records []slog.Record
}

func (r *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (r *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	r.records = append(r.records, record)

	return nil
}

func (r *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return r }
func (r *recordingHandler) WithGroup(_ string) slog.Handler      { return r }
