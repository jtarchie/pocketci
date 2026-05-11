package fly

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/superfly/fly-go/flaps"
)

// flyRetryAttempts caps how many times a single Fly API call is retried on
// transient failures (429, 5xx). The cap is small because every call site
// already pays a wait timeout on top — this is a backoff on top of, not a
// replacement for, the existing per-call timeouts.
const flyRetryAttempts = 3

// flyRetryInitialForTest and flyRetryMaxForTest are var (not const) so
// retry_test.go can compress them without sitting around for real-world
// backoff durations. Production callers should treat them as constants:
// 200ms initial doubling to 1s cap, full jitter, 3-attempt cap means
// worst-case ~1.5s of added latency before surfacing the failure.
var (
	flyRetryInitialForTest = 200 * time.Millisecond
	flyRetryMaxForTest     = time.Second
)

// flyDoWithRetry runs fn with retry+jitter on transient failures (429 and
// 5xx FlapsError responses, transient context errors are not retried).
// fn is expected to be idempotent at this layer; callers wrap it around
// API operations that are safe to repeat (Get/List/Wait/Destroy/Suspend).
// Launch and CreateVolume have side effects on success so they're left
// uncovered — a 429 there is rare and a stronger signal of upstream
// trouble that should surface to the operator.
func flyDoWithRetry[T any](ctx context.Context, logger *slog.Logger, op string, fn func() (T, error)) (T, error) {
	var zero T

	delay := flyRetryInitialForTest

	for attempt := 0; attempt < flyRetryAttempts; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}

		if ctx.Err() != nil {
			return zero, err
		}

		if !flyIsRetryable(err) {
			return zero, err
		}

		if attempt == flyRetryAttempts-1 {
			return zero, err
		}

		jittered := delay/2 + time.Duration(rand.Int64N(int64(delay)/2+1))

		if logger != nil {
			logger.Warn("fly.api.retry",
				"op", op,
				"attempt", attempt+1,
				"of", flyRetryAttempts,
				"backoff_ms", jittered.Milliseconds(),
				"err", err.Error(),
			)
		}

		select {
		case <-ctx.Done():
			return zero, err
		case <-time.After(jittered):
		}

		delay *= 2
		if delay > flyRetryMaxForTest {
			delay = flyRetryMaxForTest
		}
	}

	return zero, nil
}

// flyIsRetryable returns true when err is a transient Fly API failure that
// is safe to retry — 429 (rate-limited) or 5xx (server error). 4xx other
// than 429 surfaces a real client-side problem (bad config, auth, missing
// resource) and retrying just delays the operator finding out.
func flyIsRetryable(err error) bool {
	var fe *flaps.FlapsError
	if !errors.As(err, &fe) {
		return false
	}

	if fe.ResponseStatusCode == 429 {
		return true
	}

	if fe.ResponseStatusCode >= 500 && fe.ResponseStatusCode < 600 {
		return true
	}

	return false
}
