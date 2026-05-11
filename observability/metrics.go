package observability

// Metrics is the recording surface for runtime measurements. Three primitive
// operations (counter add, gauge set, histogram observe) cover everything we
// need — run lifecycle, queue depth, webhook receipts, driver latency —
// without locking the codebase to a specific metrics library.
//
// The (name, value, attributes) shape is intentionally aligned with the
// OpenTelemetry metric data model so a future OTel-backed implementation can
// drop in without changing call sites.
type Metrics interface {
	CounterAdd(name string, delta float64, attributes map[string]string)
	GaugeSet(name string, value float64, attributes map[string]string)
	HistogramObserve(name string, value float64, attributes map[string]string)
}

// MetricKind classifies the instrument kind of a measurement, matching the
// OpenTelemetry instrument categories (Counter / Gauge / Histogram).
type MetricKind string

const (
	MetricKindCounter   MetricKind = "counter"
	MetricKindGauge     MetricKind = "gauge"
	MetricKindHistogram MetricKind = "histogram"
)

// Event types used when forwarding measurements through Provider.Event.
// Splitting by instrument kind (rather than a single "metric" event) keeps the
// PostHog / Honeybadger Insights event taxonomy small (three names total)
// while still letting dashboards filter by counter vs gauge vs histogram
// before drilling into a specific metric_name attribute.
const (
	EventTypeMetricCounter   = "metric.counter"
	EventTypeMetricGauge     = "metric.gauge"
	EventTypeMetricHistogram = "metric.histogram"
)

// MetricEventPayloadKey lists the canonical keys used in the data map sent to
// Provider.Event. Mirrors the OpenTelemetry metric data point fields so the
// same structured event can be replayed into an OTel meter later.
const (
	MetricEventKeyName       = "metric_name"
	MetricEventKeyKind       = "kind"
	MetricEventKeyValue      = "value"
	MetricEventKeyAttributes = "attributes"
)

// noopMetrics discards every measurement. It is the default when no
// observability provider is configured so callers never need a nil-check.
type noopMetrics struct{}

// NoopMetrics returns a Metrics implementation that discards every call.
func NoopMetrics() Metrics { return noopMetrics{} }

func (noopMetrics) CounterAdd(_ string, _ float64, _ map[string]string)       {}
func (noopMetrics) GaugeSet(_ string, _ float64, _ map[string]string)         {}
func (noopMetrics) HistogramObserve(_ string, _ float64, _ map[string]string) {}

// eventMetrics translates Metrics calls into structured Provider.Event calls.
// Each measurement becomes one event whose payload mirrors the OpenTelemetry
// metric data point shape.
type eventMetrics struct {
	provider Provider
}

// NewEventMetrics returns a Metrics implementation that forwards every
// measurement to provider.Event. When provider is nil it returns NoopMetrics.
//
// The Provider.Event hook is the same one used for PostHog / Honeybadger
// Insights, so metrics flow through the existing observability pipeline with
// no extra plumbing. Future migration to an OpenTelemetry meter only requires
// swapping this implementation; the call sites in execution.go,
// scheduler/scheduler.go, etc. remain unchanged.
func NewEventMetrics(provider Provider) Metrics {
	if provider == nil {
		return NoopMetrics()
	}

	return &eventMetrics{provider: provider}
}

func (e *eventMetrics) record(eventType string, kind MetricKind, name string, value float64, attrs map[string]string) {
	payload := map[string]any{
		MetricEventKeyName:  name,
		MetricEventKeyKind:  string(kind),
		MetricEventKeyValue: value,
	}

	if len(attrs) > 0 {
		// Copy so backends are free to retain the map without aliasing the
		// caller's storage.
		copied := make(map[string]string, len(attrs))
		for k, v := range attrs {
			copied[k] = v
		}

		payload[MetricEventKeyAttributes] = copied
	}

	// Provider.Event errors are swallowed: a metric failure must not break the
	// caller's hot path. The Provider implementation is expected to log its
	// own delivery failures.
	_ = e.provider.Event(eventType, payload)
}

func (e *eventMetrics) CounterAdd(name string, delta float64, attrs map[string]string) {
	e.record(EventTypeMetricCounter, MetricKindCounter, name, delta, attrs)
}

func (e *eventMetrics) GaugeSet(name string, value float64, attrs map[string]string) {
	e.record(EventTypeMetricGauge, MetricKindGauge, name, value, attrs)
}

func (e *eventMetrics) HistogramObserve(name string, value float64, attrs map[string]string) {
	e.record(EventTypeMetricHistogram, MetricKindHistogram, name, value, attrs)
}
