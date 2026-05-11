package scheduler_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces that no goroutines leak from scheduler tests.
// The scheduler runs its own background goroutine via Start; tests must
// pair every Start with a Stop.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
