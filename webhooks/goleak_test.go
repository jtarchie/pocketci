package webhooks_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces that no goroutines leak from webhooks tests.
// Webhook detection is synchronous; any leaked goroutine is a real bug.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
