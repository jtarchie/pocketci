package runner_test

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// BenchmarkCallIndexFmtSprintf measures the current approach: fmt.Sprintf under mutex.
func BenchmarkCallIndexFmtSprintf(b *testing.B) {
	var mu sync.Mutex

	var idx int

	name := "build-and-test"

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		mu.Lock()
		stepID := fmt.Sprintf("%d-%s", idx, name)
		idx++
		mu.Unlock()

		_ = stepID
	}
}

// BenchmarkCallIndexAtomic measures the proposed approach: atomic counter + strconv.
func BenchmarkCallIndexAtomic(b *testing.B) {
	var counter atomic.Int64

	name := "build-and-test"

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		i := counter.Add(1) - 1
		stepID := strconv.FormatInt(i, 10) + "-" + name

		_ = stepID
	}
}
