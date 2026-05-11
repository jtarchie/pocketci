package observability

// Metrics is the minimal cross-cutting metric surface used by PocketCI.
// It is intentionally small — three primitive operations (counter add, gauge
// set, histogram observe) are enough to express run lifecycle, queue depth,
// webhook receipts, and driver latency without locking the codebase to a
// specific metrics library.
//
// Implementations are expected to be safe for concurrent use.
type Metrics interface {
	CounterAdd(name string, delta float64, labels map[string]string)
	GaugeSet(name string, value float64, labels map[string]string)
	HistogramObserve(name string, value float64, labels map[string]string)
}

// noopMetrics discards all measurements. It is the default when no metrics
// backend is configured so callers never need a nil-check.
type noopMetrics struct{}

// NoopMetrics returns a Metrics implementation that discards every call.
func NoopMetrics() Metrics { return noopMetrics{} }

func (noopMetrics) CounterAdd(_ string, _ float64, _ map[string]string)       {}
func (noopMetrics) GaugeSet(_ string, _ float64, _ map[string]string)         {}
func (noopMetrics) HistogramObserve(_ string, _ float64, _ map[string]string) {}
