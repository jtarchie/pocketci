package backwards

import (
	"context"
	"fmt"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
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

	var (
		g      *errgroup.Group
		gCtx   = sc.Ctx
		cancel context.CancelFunc = func() {}
	)

	if failFast {
		gCtx, cancel = context.WithCancel(sc.Ctx)
		g = &errgroup.Group{}
	} else {
		g = &errgroup.Group{}
	}

	defer cancel()

	sem := semaphore.NewWeighted(int64(limit))

	for i, innerStep := range steps {
		if err := sem.Acquire(gCtx, 1); err != nil {
			break // gCtx cancelled (fail-fast triggered by a prior goroutine error)
		}

		idx := i
		s := innerStep
		innerPrefix := fmt.Sprintf("%s/in_parallel/%s", pathPrefix, zeroPadWithLength(idx, len(steps)))

		g.Go(func() error {
			err := sc.ProcessStep(&s, innerPrefix)
			if err != nil {
				cancel() // cancel context BEFORE releasing semaphore to prevent race
			}
			sem.Release(1)
			return err
		})
	}

	runErr := g.Wait()

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status": statusFromErr(runErr),
	})
	if err != nil {
		return fmt.Errorf("storage set result: %w", err)
	}

	return runErr
}
