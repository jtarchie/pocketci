package fly

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every fly/ test under goleak so the waitForStop and
// helper-machine resume goroutines spawned by RunContainer surface any
// missed cancellation/cleanup as a test failure rather than as quiet
// long-running leaks in production. Network-keepalive goroutines from the
// underlying fly-go HTTP client are tolerated — they're persistent by
// design and outlive the Driver, matching the runtime/ package convention.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	)
}
