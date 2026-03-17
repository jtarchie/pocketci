package observability_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/observability"
	. "github.com/onsi/gomega"
)

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
