package fly

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/superfly/fly-go/flaps"
)

func testRetryLogger() *slog.Logger {
	// Discard handler keeps the retry warnings out of test output without
	// changing the logging shape the helper uses (slog.Warn level).
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// withShortRetry temporarily compresses the backoff schedule so unit tests
// don't sit around for 1+ seconds per retry case. The originals are
// restored on test cleanup.
func withShortRetry(t *testing.T) {
	t.Helper()

	origInitial := flyRetryInitialForTest
	origMax := flyRetryMaxForTest
	flyRetryInitialForTest = 1 * time.Millisecond
	flyRetryMaxForTest = 4 * time.Millisecond

	t.Cleanup(func() {
		flyRetryInitialForTest = origInitial
		flyRetryMaxForTest = origMax
	})
}

// TestFlyDoWithRetryRetriesOn429 verifies that a 429 response triggers
// retries until the call succeeds.
func TestFlyDoWithRetryRetriesOn429(t *testing.T) {
	withShortRetry(t)

	calls := 0
	_, err := flyDoWithRetry(context.Background(), testRetryLogger(), "test.op", func() (struct{}, error) {
		calls++
		if calls < 2 {
			return struct{}{}, &flaps.FlapsError{ResponseStatusCode: 429, OriginalError: errors.New("rate limited")}
		}

		return struct{}{}, nil
	})

	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected 2 calls (1 fail + 1 success), got %d", calls)
	}
}

// TestFlyDoWithRetryRetriesOn5xx verifies 5xx responses are retried.
func TestFlyDoWithRetryRetriesOn5xx(t *testing.T) {
	withShortRetry(t)

	calls := 0
	_, err := flyDoWithRetry(context.Background(), testRetryLogger(), "test.op", func() (struct{}, error) {
		calls++
		if calls < 3 {
			return struct{}{}, &flaps.FlapsError{ResponseStatusCode: 503, OriginalError: errors.New("upstream")}
		}

		return struct{}{}, nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

// TestFlyDoWithRetryDoesNotRetry4xx verifies that non-429 4xx errors fail
// immediately — those represent real client problems where retrying just
// delays the operator finding out.
func TestFlyDoWithRetryDoesNotRetry4xx(t *testing.T) {
	withShortRetry(t)

	calls := 0
	_, err := flyDoWithRetry(context.Background(), testRetryLogger(), "test.op", func() (struct{}, error) {
		calls++
		return struct{}{}, &flaps.FlapsError{ResponseStatusCode: 404, OriginalError: errors.New("not found")}
	})

	if err == nil {
		t.Fatalf("expected error to be returned on 404")
	}

	if calls != 1 {
		t.Fatalf("expected 1 call (no retry on 4xx), got %d", calls)
	}
}

// TestFlyDoWithRetryDoesNotRetryNonFlaps verifies that random non-Flaps
// errors aren't retried; they likely represent local programming errors
// (nil deref, context cancellation, etc.) where retry won't help.
func TestFlyDoWithRetryDoesNotRetryNonFlaps(t *testing.T) {
	withShortRetry(t)

	calls := 0
	_, err := flyDoWithRetry(context.Background(), testRetryLogger(), "test.op", func() (struct{}, error) {
		calls++
		return struct{}{}, errors.New("local failure")
	})

	if err == nil {
		t.Fatalf("expected error to be returned")
	}

	if calls != 1 {
		t.Fatalf("expected 1 call (no retry on non-Flaps error), got %d", calls)
	}
}

// TestFlyDoWithRetryGivesUpAfterMaxAttempts verifies the call cap holds.
func TestFlyDoWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	withShortRetry(t)

	calls := 0
	_, err := flyDoWithRetry(context.Background(), testRetryLogger(), "test.op", func() (struct{}, error) {
		calls++
		return struct{}{}, &flaps.FlapsError{ResponseStatusCode: 429, OriginalError: errors.New("rate limited")}
	})

	if err == nil {
		t.Fatalf("expected error after exhausting retries")
	}

	if calls != flyRetryAttempts {
		t.Fatalf("expected %d calls, got %d", flyRetryAttempts, calls)
	}
}

// TestFlyDoWithRetryRespectsContext verifies cancelling the context stops
// retries promptly rather than waiting out the remaining backoff.
func TestFlyDoWithRetryRespectsContext(t *testing.T) {
	// Use the default (longer) backoff so the cancel actually has work
	// to interrupt. 1ms backoff would finish before the goroutine fires.
	origInitial := flyRetryInitialForTest
	origMax := flyRetryMaxForTest
	flyRetryInitialForTest = 100 * time.Millisecond
	flyRetryMaxForTest = 200 * time.Millisecond

	t.Cleanup(func() {
		flyRetryInitialForTest = origInitial
		flyRetryMaxForTest = origMax
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	calls := 0

	_, err := flyDoWithRetry(ctx, testRetryLogger(), "test.op", func() (struct{}, error) {
		calls++
		return struct{}{}, &flaps.FlapsError{ResponseStatusCode: 429, OriginalError: errors.New("rate limited")}
	})

	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error when context is cancelled")
	}

	if calls > 2 {
		t.Fatalf("expected ≤2 calls before context cancel, got %d", calls)
	}

	// Cancellation should short-circuit well before the full backoff
	// schedule would have completed (~3*200ms = 600ms).
	if elapsed > 500*time.Millisecond {
		t.Fatalf("retry did not respect context cancellation: took %v", elapsed)
	}
}
