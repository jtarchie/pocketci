package backwards

import (
	"fmt"
	"sync"
	"sync/atomic"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
)

// InParallelHandler executes inner steps concurrently with an optional
// concurrency limit and fail-fast semantics.
type InParallelHandler struct{}

func (h *InParallelHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	storageKey := fmt.Sprintf("%s/%s/in_parallel", sc.BaseStorageKey(), pathPrefix)

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": "pending",
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	steps := step.InParallel.Steps
	limit := resolveLimit(sc, step.InParallel.Limit, len(steps))
	failFast := step.InParallel.FailFast

	// Pre-populate ExecutedTasks in declaration order for direct task steps
	// so that assertion ordering is deterministic even under concurrent execution.
	// Mark them as pre-registered so TaskHandler won't append them a second time.
	// Only safe when fail_fast is false — with fail_fast some tasks may be skipped
	// and should not appear in ExecutedTasks.
	if !failFast {
		sc.ExecutedTasksMu.Lock()
		for _, s := range steps {
			if s.Task != "" {
				sc.ExecutedTasks = append(sc.ExecutedTasks, s.Task)
				if sc.PreRegisteredTasks == nil {
					sc.PreRegisteredTasks = make(map[string]bool)
				}
				sc.PreRegisteredTasks[s.Task] = true
			}
		}
		sc.ExecutedTasksMu.Unlock()
	}

	sem := make(chan struct{}, limit)

	var wg sync.WaitGroup

	var mu sync.Mutex

	var firstErr error

	var failed atomic.Bool

	for i, innerStep := range steps {
		if failFast && failed.Load() {
			break
		}

		sem <- struct{}{} // acquire semaphore slot

		// Re-check after acquiring: another goroutine may have failed while we waited.
		if failFast && failed.Load() {
			<-sem

			break
		}

		wg.Add(1)

		go func(idx int, s config.Step) {
			defer wg.Done()
			defer func() { <-sem }() // release slot

			innerPrefix := fmt.Sprintf("%s/in_parallel/%s", pathPrefix, zeroPadWithLength(idx, len(steps)))
			stepErr := sc.ProcessStep(&s, innerPrefix)

			if stepErr != nil {
				mu.Lock()
				firstErr = higherPriorityError(firstErr, stepErr)
				mu.Unlock()

				failed.Store(true)
			}
		}(i, innerStep)
	}

	wg.Wait()

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": statusFromErr(firstErr),
	})
	if err != nil {
		return fmt.Errorf("storage set result: %w", err)
	}

	return firstErr
}

// errorPriority returns a numeric priority for error types.
// Higher values take precedence: abort > errored > failed.
func errorPriority(err error) int {
	switch {
	case isAbortError(err):
		return 3
	case isErroredError(err):
		return 2
	case isFailedError(err):
		return 1
	default:
		return 0
	}
}

// higherPriorityError returns the error with higher priority.
// If existing is nil, incoming is always returned.
func higherPriorityError(existing, incoming error) error {
	if existing == nil {
		return incoming
	}

	if errorPriority(incoming) > errorPriority(existing) {
		return incoming
	}

	return existing
}
