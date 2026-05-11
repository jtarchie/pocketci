package observability_test

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/jtarchie/pocketci/observability"
	. "github.com/onsi/gomega"
)

// recordingProvider captures every Event call for assertion. Implements
// observability.Provider; the slog and Close hooks are no-ops because metric
// tests don't exercise them.
type recordingProvider struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	eventType string
	data      map[string]any
}

func (r *recordingProvider) Name() string { return "recording" }

func (r *recordingProvider) Event(eventType string, data map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, recordedEvent{eventType: eventType, data: data})

	return nil
}

func (r *recordingProvider) SlogHandler(next slog.Handler) slog.Handler { return next }

func (r *recordingProvider) Close() error { return nil }

func (r *recordingProvider) snapshot() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]recordedEvent, len(r.events))
	copy(out, r.events)

	return out
}

func TestNoopMetricsDiscardsEverything(t *testing.T) {
	t.Parallel()

	m := observability.NoopMetrics()

	// Should not panic, allocate, or otherwise misbehave.
	m.CounterAdd("anything", 5, map[string]string{"a": "b"})
	m.GaugeSet("anything", 1, nil)
	m.HistogramObserve("anything", 0.5, map[string]string{"x": "y"})
}

func TestNewEventMetricsWithNilProviderReturnsNoop(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	m := observability.NewEventMetrics(nil)
	assert.Expect(m).NotTo(BeNil())

	// No provider to record into; just assert no panic on use.
	m.CounterAdd("x", 1, nil)
}

func TestEventMetricsForwardsCounterAsEvent(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	provider := &recordingProvider{}
	m := observability.NewEventMetrics(provider)

	m.CounterAdd("pocketci_runs_total", 1, map[string]string{"status": "success"})

	events := provider.snapshot()
	assert.Expect(events).To(HaveLen(1))
	assert.Expect(events[0].eventType).To(Equal(observability.EventTypeMetricCounter))
	assert.Expect(events[0].data[observability.MetricEventKeyName]).To(Equal("pocketci_runs_total"))
	assert.Expect(events[0].data[observability.MetricEventKeyKind]).To(Equal(string(observability.MetricKindCounter)))
	assert.Expect(events[0].data[observability.MetricEventKeyValue]).To(Equal(float64(1)))

	attrs, ok := events[0].data[observability.MetricEventKeyAttributes].(map[string]string)
	assert.Expect(ok).To(BeTrue())
	assert.Expect(attrs).To(Equal(map[string]string{"status": "success"}))
}

func TestEventMetricsGaugeAndHistogramUseDistinctEventTypes(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	provider := &recordingProvider{}
	m := observability.NewEventMetrics(provider)

	m.GaugeSet("pocketci_queue_depth", 7, nil)
	m.HistogramObserve("pocketci_run_duration_seconds", 1.2, map[string]string{"status": "success"})

	events := provider.snapshot()
	assert.Expect(events).To(HaveLen(2))

	assert.Expect(events[0].eventType).To(Equal(observability.EventTypeMetricGauge))
	assert.Expect(events[0].data[observability.MetricEventKeyValue]).To(Equal(float64(7)))
	assert.Expect(events[0].data).NotTo(HaveKey(observability.MetricEventKeyAttributes))

	assert.Expect(events[1].eventType).To(Equal(observability.EventTypeMetricHistogram))
	assert.Expect(events[1].data[observability.MetricEventKeyValue]).To(Equal(1.2))
}

func TestEventMetricsCopiesAttributesSoCallerMutationDoesNotLeak(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	provider := &recordingProvider{}
	m := observability.NewEventMetrics(provider)

	attrs := map[string]string{"status": "success"}
	m.CounterAdd("metric", 1, attrs)

	// Mutate the caller-side map after the call returns. The recorded event
	// must not reflect the mutation.
	attrs["status"] = "mutated"

	events := provider.snapshot()
	recorded := events[0].data[observability.MetricEventKeyAttributes].(map[string]string)
	assert.Expect(recorded["status"]).To(Equal("success"))
}
