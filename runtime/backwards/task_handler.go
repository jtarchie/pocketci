package backwards

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/storage"
)

// TaskHandler executes task steps by running containers.
type TaskHandler struct{}

func (h *TaskHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	if step.Parallelism > 1 {
		return h.executeParallel(sc, step, pathPrefix)
	}

	taskName := step.Task

	sc.ExecutedTasksMu.Lock()
	sc.ExecutedTasks = append(sc.ExecutedTasks, taskName)
	sc.ExecutedTasksMu.Unlock()

	return h.runTask(sc, step, pathPrefix, taskName, step.TaskConfig.Env)
}

func (h *TaskHandler) executeParallel(sc *StepContext, step *config.Step, pathPrefix string) error {
	count := step.Parallelism
	limit := resolveLimit(sc, 0, count)
	sem := make(chan struct{}, limit)

	// Pre-populate ExecutedTasks for deterministic assertion order.
	for i := 1; i <= count; i++ {
		indexedName := fmt.Sprintf("%s-%d", step.Task, i)

		sc.ExecutedTasksMu.Lock()
		sc.ExecutedTasks = append(sc.ExecutedTasks, indexedName)
		sc.ExecutedTasksMu.Unlock()
	}

	var wg sync.WaitGroup

	var mu sync.Mutex

	var firstErr error

	for i := 1; i <= count; i++ {
		sem <- struct{}{} // acquire semaphore slot

		wg.Add(1)

		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()

			indexedName := fmt.Sprintf("%s-%d", step.Task, index)
			env := cloneEnv(step.TaskConfig.Env)
			env["CI_TASK_INDEX"] = strconv.Itoa(index)
			env["CI_TASK_COUNT"] = strconv.Itoa(count)

			err := h.runTask(sc, step, pathPrefix, indexedName, env)
			if err != nil {
				mu.Lock()
				firstErr = higherPriorityError(firstErr, err)
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	return firstErr
}

// runTask executes a single container task with the given name and environment.
func (h *TaskHandler) runTask(sc *StepContext, step *config.Step, pathPrefix, taskName string, env map[string]string) error {
	storageKey := fmt.Sprintf("%s/%s/tasks/%s", sc.BaseStorageKey(), pathPrefix, taskName)

	startedAt := time.Now()

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "pending",
		"started_at": startedAt.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	task := orchestra.Task{
		ID:      fmt.Sprintf("%s-%s", sc.JobName, taskName),
		Command: buildCommand(step.TaskConfig),
		Env:     env,
		Image:   resolveImage(step.TaskConfig),
	}

	execCtx := sc.Ctx
	if step.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(sc.Ctx, step.Timeout)

		defer cancel()
	}

	container, err := sc.Driver.RunContainer(sc.Ctx, task)
	if err != nil {
		return &TaskErroredError{TaskName: taskName, Err: err}
	}

	defer func() { _ = container.Cleanup(sc.Ctx) }()

	status, err := waitForContainer(execCtx, container)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			elapsed := time.Since(startedAt)

			_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
				"status":     "abort",
				"started_at": startedAt.Format(time.RFC3339),
				"elapsed":    elapsed.String(),
			})

			return &TaskAbortedError{TaskName: taskName}
		}

		return &TaskErroredError{TaskName: taskName, Err: err}
	}

	exitCode := status.ExitCode()
	elapsed := time.Since(startedAt)

	var stdout bytes.Buffer

	err = container.Logs(sc.Ctx, &stdout, &stdout, false)
	if err != nil {
		sc.Logger.Error("task.logs.error", "task", taskName, "err", err)
	}

	resultStatus := "success"
	if exitCode != 0 {
		resultStatus = "failure"
		sc.Logger.Debug("task.failed", "task", taskName, "code", exitCode)
	}

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     resultStatus,
		"code":       exitCode,
		"started_at": startedAt.Format(time.RFC3339),
		"elapsed":    elapsed.String(),
	})
	if err != nil {
		return fmt.Errorf("storage set result: %w", err)
	}

	if step.Assert != nil && step.Assert.Code != nil {
		if exitCode != *step.Assert.Code {
			return &AssertionError{
				Message: fmt.Sprintf("task %q: expected exit code %d, got %d", taskName, *step.Assert.Code, exitCode),
			}
		}
	}

	if step.Assert != nil && step.Assert.Stdout != "" {
		if !strings.Contains(stdout.String(), step.Assert.Stdout) {
			return &AssertionError{
				Message: fmt.Sprintf("task %q: stdout does not contain %q", taskName, step.Assert.Stdout),
			}
		}
	}

	if exitCode != 0 {
		return &TaskFailedError{TaskName: taskName, Code: exitCode}
	}

	return nil
}

func cloneEnv(original map[string]string) map[string]string {
	env := make(map[string]string, len(original)+2)
	for k, v := range original {
		env[k] = v
	}

	return env
}

func resolveImage(cfg *config.TaskConfig) string {
	if cfg == nil {
		return ""
	}

	if cfg.Image != "" {
		return cfg.Image
	}

	if repo, ok := cfg.ImageResource.Source["repository"].(string); ok {
		return repo
	}

	return ""
}

func buildCommand(cfg *config.TaskConfig) []string {
	if cfg == nil || cfg.Run == nil {
		return nil
	}

	cmd := []string{cfg.Run.Path}
	cmd = append(cmd, cfg.Run.Args...)

	return cmd
}

func waitForContainer(ctx context.Context, container orchestra.Container) (orchestra.ContainerStatus, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
			status, err := container.Status(ctx)
			if err != nil {
				return nil, fmt.Errorf("container status: %w", err)
			}

			if status.IsDone() {
				return status, nil
			}

			time.Sleep(10 * time.Millisecond)
		}
	}
}
