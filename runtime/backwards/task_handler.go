package backwards

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/storage"
)

// TaskHandler executes task steps by running containers.
type TaskHandler struct{}

func (h *TaskHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	taskName := step.Task
	sc.ExecutedTasks = append(sc.ExecutedTasks, taskName)

	storageKey := fmt.Sprintf("%s/%s/tasks/%s", sc.BaseStorageKey(), pathPrefix, taskName)

	startedAt := time.Now()

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "pending",
		"started_at": startedAt.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	command := buildCommand(step.TaskConfig)

	task := orchestra.Task{
		ID:      fmt.Sprintf("%s-%s", sc.JobName, taskName),
		Command: command,
		Env:     step.TaskConfig.Env,
		Image:   resolveImage(step.TaskConfig),
	}

	// Use a timeout context if configured.
	execCtx := sc.Ctx
	if step.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(sc.Ctx, step.Timeout)

		defer cancel()
	}

	container, err := sc.Driver.RunContainer(sc.Ctx, task)
	if err != nil {
		return fmt.Errorf("run container for task %q: %w", taskName, err)
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

		return fmt.Errorf("wait for task %q: %w", taskName, err)
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

	if exitCode != 0 {
		return &TaskFailedError{TaskName: taskName, Code: exitCode}
	}

	return nil
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
