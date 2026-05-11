package observability_test

import (
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/observability"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNoopMetricsDiscardsEverything(t *testing.T) {
	t.Parallel()

	m := observability.NoopMetrics()

	// Should not panic, allocate, or otherwise misbehave.
	m.CounterAdd("anything", 5, map[string]string{"a": "b"})
	m.GaugeSet("anything", 1, nil)
	m.HistogramObserve("anything", 0.5, map[string]string{"x": "y"})
}

func TestPromMetricsCountersAndGaugesAccumulate(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	reg := prometheus.NewRegistry()
	m := observability.NewPromMetrics(reg)

	m.CounterAdd("pocketci_runs_total", 1, map[string]string{"status": "success"})
	m.CounterAdd("pocketci_runs_total", 2, map[string]string{"status": "success"})
	m.CounterAdd("pocketci_runs_total", 1, map[string]string{"status": "failure"})

	m.GaugeSet("pocketci_queue_depth", 7, nil)
	m.GaugeSet("pocketci_queue_depth", 3, nil)

	expected := `
# HELP pocketci_queue_depth
# TYPE pocketci_queue_depth gauge
pocketci_queue_depth 3
# HELP pocketci_runs_total
# TYPE pocketci_runs_total counter
pocketci_runs_total{status="failure"} 1
pocketci_runs_total{status="success"} 3
`
	err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "pocketci_queue_depth", "pocketci_runs_total")
	assert.Expect(err).NotTo(HaveOccurred())
}

func TestPromMetricsHistogramObserves(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	reg := prometheus.NewRegistry()
	m := observability.NewPromMetrics(reg)

	m.HistogramObserve("pocketci_run_duration_seconds", 1.2, map[string]string{"status": "success"})
	m.HistogramObserve("pocketci_run_duration_seconds", 2.4, map[string]string{"status": "success"})

	mfs, err := reg.Gather()
	assert.Expect(err).NotTo(HaveOccurred())

	var found bool

	for _, mf := range mfs {
		if mf.GetName() != "pocketci_run_duration_seconds" {
			continue
		}

		found = true

		assert.Expect(mf.GetMetric()).To(HaveLen(1))
		assert.Expect(mf.GetMetric()[0].GetHistogram().GetSampleCount()).To(BeNumerically("==", 2))
		assert.Expect(mf.GetMetric()[0].GetHistogram().GetSampleSum()).To(BeNumerically("~", 3.6, 0.0001))
	}

	assert.Expect(found).To(BeTrue())
}

func TestPromMetricsConcurrentUseDoesNotRace(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	m := observability.NewPromMetrics(reg)

	done := make(chan struct{})

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				m.CounterAdd("c", 1, map[string]string{"k": "v"})
				m.GaugeSet("g", float64(j), nil)
				m.HistogramObserve("h", 0.1, nil)
			}

			done <- struct{}{}
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

