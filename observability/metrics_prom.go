package observability

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// PromMetrics is a Prometheus-backed Metrics implementation. It auto-creates
// counter/gauge/histogram vectors on first use, registering them against the
// supplied registry (defaults to prometheus.DefaultRegisterer when nil), so
// callers do not have to declare metrics ahead of time.
type PromMetrics struct {
	registerer prometheus.Registerer

	mu         sync.RWMutex
	counters   map[string]*prometheus.CounterVec
	gauges     map[string]*prometheus.GaugeVec
	histograms map[string]*prometheus.HistogramVec
}

// NewPromMetrics returns a PromMetrics that registers vectors against
// registerer. When registerer is nil the default Prometheus registry is used.
func NewPromMetrics(registerer prometheus.Registerer) *PromMetrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	return &PromMetrics{
		registerer: registerer,
		counters:   map[string]*prometheus.CounterVec{},
		gauges:     map[string]*prometheus.GaugeVec{},
		histograms: map[string]*prometheus.HistogramVec{},
	}
}

// labelKey returns a stable cache key for the (metric, label-set) tuple.
// Different label key-sets need separate vectors because Prometheus vectors
// are constructed with a fixed label-name list.
func labelKey(name string, labels map[string]string) (string, []string) {
	if len(labels) == 0 {
		return name, nil
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return name + "{" + strings.Join(keys, ",") + "}", keys
}

func orderedValues(labels map[string]string, keys []string) []string {
	values := make([]string, len(keys))
	for i, k := range keys {
		values[i] = labels[k]
	}

	return values
}

func (p *PromMetrics) counter(name string, labels map[string]string) (*prometheus.CounterVec, []string) {
	key, keys := labelKey(name, labels)

	p.mu.RLock()
	vec, ok := p.counters[key]
	p.mu.RUnlock()

	if ok {
		return vec, keys
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if vec, ok = p.counters[key]; ok {
		return vec, keys
	}

	vec = prometheus.NewCounterVec(prometheus.CounterOpts{Name: name}, keys)

	err := p.registerer.Register(vec)
	if err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			vec, _ = are.ExistingCollector.(*prometheus.CounterVec)
		}
	}

	p.counters[key] = vec

	return vec, keys
}

func (p *PromMetrics) gauge(name string, labels map[string]string) (*prometheus.GaugeVec, []string) {
	key, keys := labelKey(name, labels)

	p.mu.RLock()
	vec, ok := p.gauges[key]
	p.mu.RUnlock()

	if ok {
		return vec, keys
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if vec, ok = p.gauges[key]; ok {
		return vec, keys
	}

	vec = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name}, keys)

	err := p.registerer.Register(vec)
	if err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			vec, _ = are.ExistingCollector.(*prometheus.GaugeVec)
		}
	}

	p.gauges[key] = vec

	return vec, keys
}

func (p *PromMetrics) histogram(name string, labels map[string]string) (*prometheus.HistogramVec, []string) {
	key, keys := labelKey(name, labels)

	p.mu.RLock()
	vec, ok := p.histograms[key]
	p.mu.RUnlock()

	if ok {
		return vec, keys
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if vec, ok = p.histograms[key]; ok {
		return vec, keys
	}

	vec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    name,
		Buckets: prometheus.DefBuckets,
	}, keys)

	err := p.registerer.Register(vec)
	if err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			vec, _ = are.ExistingCollector.(*prometheus.HistogramVec)
		}
	}

	p.histograms[key] = vec

	return vec, keys
}

// CounterAdd increments the named counter by delta.
func (p *PromMetrics) CounterAdd(name string, delta float64, labels map[string]string) {
	vec, keys := p.counter(name, labels)
	if vec == nil {
		return
	}

	vec.WithLabelValues(orderedValues(labels, keys)...).Add(delta)
}

// GaugeSet sets the named gauge to value.
func (p *PromMetrics) GaugeSet(name string, value float64, labels map[string]string) {
	vec, keys := p.gauge(name, labels)
	if vec == nil {
		return
	}

	vec.WithLabelValues(orderedValues(labels, keys)...).Set(value)
}

// HistogramObserve records value into the named histogram.
func (p *PromMetrics) HistogramObserve(name string, value float64, labels map[string]string) {
	vec, keys := p.histogram(name, labels)
	if vec == nil {
		return
	}

	vec.WithLabelValues(orderedValues(labels, keys)...).Observe(value)
}
