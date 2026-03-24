package runner

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

// AgentFunc is a function that runs an LLM agent. It takes config as raw JSON
// and returns the result as raw JSON. This is injected by the runtime layer
// to avoid import cycles between runner and agent packages.
type AgentFunc func(configJSON json.RawMessage) (json.RawMessage, error)

type PipelineRunner struct {
	client           orchestra.Driver
	storage          storage.Driver
	ctx              context.Context //nolint: containedctx
	logger           *slog.Logger
	volumes          []orchestra.Volume
	namespace        string
	runID            string
	mu               sync.Mutex // Protects callIndex
	callIndex        int        // Tracks how many times Run() has been called
	secretsManager   secrets.Manager
	pipelineID       string
	secretValues     []string                    // Cached secret values for redaction
	preseededVolumes map[string]orchestra.Volume // volume name → pre-created volume
	outputCallback   OutputCallback              // Global output callback for all tasks
	agentFunc        AgentFunc                   // Injected agent execution function
}

func NewPipelineRunner(
	ctx context.Context,
	client orchestra.Driver,
	storageClient storage.Driver,
	logger *slog.Logger,
	namespace string,
	runID string,
) *PipelineRunner {
	return &PipelineRunner{
		client:    client,
		storage:   storageClient,
		ctx:       ctx,
		logger:    logger.WithGroup("pipeline.run"),
		volumes:   []orchestra.Volume{},
		namespace: namespace,
		runID:     runID,
	}
}

// SetPreseededVolumes configures the pipeline runner to reuse pre-created,
// already-seeded volumes when CreateVolume is called for a matching name.
func (c *PipelineRunner) SetPreseededVolumes(vols map[string]orchestra.Volume) {
	c.preseededVolumes = vols
}

// SetOutputCallback sets a global output callback that is applied to every
// task run by this pipeline runner. Individual tasks may override this via
// their own OnOutput field.
func (c *PipelineRunner) SetOutputCallback(cb OutputCallback) {
	c.outputCallback = cb
}

// SetAgentFunc sets the function used to execute agent steps.
func (c *PipelineRunner) SetAgentFunc(fn AgentFunc) {
	c.agentFunc = fn
}

// RunAgent executes an LLM agent step via the injected AgentFunc.
func (c *PipelineRunner) RunAgent(configJSON json.RawMessage) (json.RawMessage, error) {
	if c.agentFunc == nil {
		return nil, errors.New("agent execution not configured on this runner")
	}

	return c.agentFunc(configJSON)
}

// SetSecretsManager configures the pipeline runner to load secrets
// from the given manager for the specified pipeline.
func (c *PipelineRunner) SetSecretsManager(mgr secrets.Manager, pipelineID string) {
	c.secretsManager = mgr
	c.pipelineID = pipelineID
}

// loadSecrets loads all secrets for this pipeline from the secrets manager
// and returns them as a map of key->value. It checks pipeline scope first,
// then falls back to global scope.
func (c *PipelineRunner) loadSecrets(ctx context.Context, requestedKeys []string) (map[string]string, error) {
	if c.secretsManager == nil || len(requestedKeys) == 0 {
		return nil, nil
	}

	result := make(map[string]string, len(requestedKeys))
	pipelineScope := secrets.PipelineScope(c.pipelineID)

	for _, key := range requestedKeys {
		// Try pipeline scope first
		val, err := c.secretsManager.Get(ctx, pipelineScope, key)
		if err == nil {
			result[key] = val
			continue
		}

		if !errors.Is(err, secrets.ErrNotFound) {
			return nil, fmt.Errorf("could not retrieve secret %q from scope %q: %w", key, pipelineScope, err)
		}

		// Fall back to global scope
		val, err = c.secretsManager.Get(ctx, secrets.GlobalScope, key)
		if err == nil {
			result[key] = val
			continue
		}

		if !errors.Is(err, secrets.ErrNotFound) {
			return nil, fmt.Errorf("could not retrieve secret %q from scope %q: %w", key, secrets.GlobalScope, err)
		}

		// Secret not found in any scope - fail fast
		return nil, fmt.Errorf("secret %q not found in scopes %q or %q: %w", key, pipelineScope, secrets.GlobalScope, secrets.ErrNotFound)
	}

	return result, nil
}

type VolumeInput struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

type VolumeResult struct {
	volume orchestra.Volume
	Name   string `json:"name"`
	Path   string `json:"path"`
}

func (c *PipelineRunner) CreateVolume(input VolumeInput) (*VolumeResult, error) {
	ctx := c.ctx

	logger := c.logger
	logger.Debug("volume.create.pipeline.request", "input", input)

	// If a pre-created volume exists for this name, reuse it.
	if c.preseededVolumes != nil {
		if vol, ok := c.preseededVolumes[input.Name]; ok && vol != nil {
			logger.Info("volume.create.reuse_preseeded", "volume", input.Name)

			c.volumes = append(c.volumes, vol)

			return &VolumeResult{
				volume: vol,
				Name:   vol.Name(),
				Path:   vol.Path(),
			}, nil
		}
	}

	volume, err := c.client.CreateVolume(ctx, input.Name, input.Size)
	if err != nil {
		logger.Error("volume.create.pipeline.error", "err", err)

		return nil, fmt.Errorf("could not create volume: %w", err)
	}

	// Track volume for cleanup
	c.volumes = append(c.volumes, volume)

	return &VolumeResult{
		volume: volume,
		Name:   volume.Name(),
		Path:   volume.Path(),
	}, nil
}

// ReadFilesFromVolume reads specific files from a volume via the driver's
// VolumeDataAccessor interface, untars the result, and returns file contents
// as a map of relative path to string content.
func (c *PipelineRunner) ReadFilesFromVolume(volumeName string, filePaths ...string) (map[string]string, error) {
	accessor, ok := c.client.(cache.VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("driver %q does not support reading files from volumes", c.client.Name())
	}

	reader, err := accessor.ReadFilesFromVolume(c.ctx, volumeName, filePaths...)
	if err != nil {
		return nil, fmt.Errorf("could not read files from volume %q: %w", volumeName, err)
	}

	defer func() { _ = reader.Close() }()

	result := make(map[string]string)
	tr := tar.NewReader(reader)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read tar entry: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		var buf strings.Builder

		if _, err := io.Copy(&buf, tr); err != nil {
			return nil, fmt.Errorf("failed to read file %q from tar: %w", header.Name, err)
		}

		result[header.Name] = buf.String()
	}

	return result, nil
}

type RunResult struct {
	Code   int    `json:"code"`
	Stderr string `json:"stderr"`
	Stdout string `json:"stdout"`

	Status RunStatus `json:"status"`
}

type TaskLogEntry struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// AppendLogEntry appends a log chunk to the entries slice, condensing
// consecutive entries of the same stream type into one entry.
func AppendLogEntry(logs []TaskLogEntry, stream, data string) []TaskLogEntry {
	if data == "" {
		return logs
	}

	if n := len(logs); n > 0 && logs[n-1].Type == stream {
		logs[n-1].Content += data
	} else {
		logs = append(logs, TaskLogEntry{Type: stream, Content: data})
	}

	return logs
}

// OutputCallback is called with streaming output chunks.
// stream is either "stdout" or "stderr", data is the output chunk.
type OutputCallback func(stream string, data string)

type RunInput struct {
	Command struct {
		Path string   `json:"path"`
		Args []string `json:"args"`
		User string   `json:"user"`
	} `json:"command"`
	ContainerLimits struct {
		CPU    int64 `json:"cpu"`
		Memory int64 `json:"memory"`
	} `json:"container_limits"`
	Env        map[string]string       `json:"env"`
	Image      string                  `json:"image"`
	Mounts     map[string]VolumeResult `json:"mounts"`
	Name       string                  `json:"name"`
	Privileged bool                    `json:"privileged"`
	Stdin      string                  `json:"stdin"`
	WorkDir    string                  `json:"work_dir"`
	// OnOutput is called with streaming output chunks as the container runs.
	// If provided, the callback receives (stream, data) where stream is "stdout" or "stderr".
	OnOutput OutputCallback `json:"-"` // Not serialized from JS, set programmatically
	// has to be string because goja doesn't support string -> time.Duration
	Timeout string `json:"timeout"`
	// StorageKey overrides the auto-generated tasks/<callIndex>-<name> storage path.
	// When set (e.g. by the backwards TS layer), Go uses this path so that the
	// caller's own storage entry is the single source of truth and no duplicate
	// top-level tasks/ entry is created.
	StorageKey string `json:"storage_key"`
}

type RunStatus string

const (
	RunAbort    RunStatus = "abort"
	RunComplete RunStatus = "complete"
)

func (c *PipelineRunner) Run(input RunInput) (*RunResult, error) {
	ctx := c.ctx

	if input.Timeout != "" {
		timeout, err := time.ParseDuration(input.Timeout)
		if err != nil {
			return nil, fmt.Errorf("could not parse timeout: %w", err)
		}

		if timeout > 0 {
			var cancel context.CancelFunc

			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}

	c.mu.Lock()
	stepID := fmt.Sprintf("%d-%s", c.callIndex, input.Name)
	c.callIndex++
	c.mu.Unlock()
	taskID := support.DeterministicTaskID(c.namespace, c.runID, stepID, input.Name)

	logger := c.logger.With("task.id", taskID, "task.name", input.Name, "task.privileged", input.Privileged)

	effectiveStorageKey := c.taskStorageKey(stepID)
	if input.StorageKey != "" {
		effectiveStorageKey = input.StorageKey
	}

	if err := c.injectSecrets(ctx, &input, effectiveStorageKey); err != nil {
		return nil, err
	}

	if input.OnOutput == nil && c.outputCallback != nil {
		input.OnOutput = c.outputCallback
	}

	logger.Info("container.run.start", "image", input.Image, "command", append([]string{input.Command.Path}, input.Command.Args...))

	storageKey := effectiveStorageKey
	c.setTaskStatus(storageKey, map[string]any{"status": "pending"})

	container, err := c.createRunContainer(ctx, taskID, input)
	if err != nil {
		logger.Error("container.run.create_error", "err", err)

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			c.setTaskStatus(storageKey, map[string]any{"status": "abort"})

			return &RunResult{Status: RunAbort}, nil
		}

		c.setTaskStatus(storageKey, map[string]any{"status": "error"})

		return nil, fmt.Errorf("could not run container: %w", err)
	}

	return c.waitAndFinalizeRun(ctx, container, input, logger, storageKey)
}

// injectSecrets resolves "secret:KEY" references in input.Env with actual
// secret values and tracks them for redaction.
func (c *PipelineRunner) injectSecrets(ctx context.Context, input *RunInput, storageKey string) error {
	if c.secretsManager == nil || input.Env == nil {
		return nil
	}

	var secretKeys []string

	for _, val := range input.Env {
		if strings.HasPrefix(val, "secret:") {
			secretKeys = append(secretKeys, strings.TrimPrefix(val, "secret:"))
		}
	}

	if len(secretKeys) == 0 {
		return nil
	}

	secretMap, err := c.loadSecrets(ctx, secretKeys)
	if err != nil {
		c.setTaskStatus(storageKey, map[string]any{
			"status": "error",
			"logs": []TaskLogEntry{{
				Type:    "stderr",
				Content: err.Error(),
			}},
		})

		return fmt.Errorf("failed to load secrets for task %q: %w", input.Name, err)
	}

	for envKey, envVal := range input.Env {
		if strings.HasPrefix(envVal, "secret:") {
			secretKey := strings.TrimPrefix(envVal, "secret:")
			if secretVal, ok := secretMap[secretKey]; ok {
				input.Env[envKey] = secretVal
				c.secretValues = append(c.secretValues, secretVal)
			}
		}
	}

	return nil
}

// createRunContainer builds mounts/command from input and runs the container.
func (c *PipelineRunner) createRunContainer(ctx context.Context, taskID string, input RunInput) (orchestra.Container, error) {
	mounts := make(orchestra.Mounts, 0, len(input.Mounts))
	for path, volume := range input.Mounts {
		mounts = append(mounts, orchestra.Mount{
			Name: volume.Name,
			Path: path,
		})
	}

	c.logger.Debug("container.run.mounts", "mounts", mounts)

	command := make([]string, 0, 1+len(input.Command.Args))
	command = append(command, input.Command.Path)
	command = append(command, input.Command.Args...)

	var stdinReader io.Reader
	if input.Stdin != "" {
		stdinReader = strings.NewReader(input.Stdin)
	}

	return c.client.RunContainer(
		ctx,
		orchestra.Task{
			Command: command,
			ContainerLimits: orchestra.ContainerLimits{
				CPU:    input.ContainerLimits.CPU,
				Memory: input.ContainerLimits.Memory,
			},
			Env:        input.Env,
			ID:         fmt.Sprintf("%s-%s", input.Name, taskID),
			Image:      input.Image,
			Mounts:     mounts,
			Privileged: input.Privileged,
			Stdin:      stdinReader,
			User:       input.Command.User,
			WorkDir:    input.WorkDir,
		},
	)
}

// waitAndFinalizeRun waits for the container to finish, collects logs, redacts
// secrets, and persists the final task status.
func (c *PipelineRunner) waitAndFinalizeRun(
	ctx context.Context,
	container orchestra.Container,
	input RunInput,
	logger *slog.Logger,
	storageKey string,
) (*RunResult, error) {
	taskStartedAt := time.Now()
	c.setTaskStatus(storageKey, map[string]any{
		"status":     "running",
		"started_at": taskStartedAt.UTC().Format(time.RFC3339),
	})

	logs := make([]TaskLogEntry, 0, 32)
	var logsMu sync.Mutex

	appendLog := func(stream, data string) {
		logsMu.Lock()
		logs = AppendLogEntry(logs, stream, data)
		logsMu.Unlock()
	}

	streamCallback := OutputCallback(func(stream, data string) {
		appendLog(stream, data)

		if input.OnOutput != nil {
			input.OnOutput(stream, data)
		}
	})

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	var streamWg sync.WaitGroup

	if input.OnOutput != nil {
		streamWg.Go(func() {
			c.streamLogsWithCallback(streamCtx, container, streamCallback, stdout, stderr)
		})
	}

	containerStatus, err := c.pollContainerStatus(ctx, container, storageKey, taskStartedAt, cancelStream)
	if err != nil {
		return nil, err
	}
	if containerStatus == nil {
		return &RunResult{Status: RunAbort}, nil
	}

	cancelStream()
	streamWg.Wait()

	finalStdout, finalStderr := &strings.Builder{}, &strings.Builder{}
	err = container.Logs(ctx, finalStdout, finalStderr, false)
	if err != nil {
		logger.Error("container.logs.error", "err", err)

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			c.setTaskStatus(storageKey, map[string]any{
				"status":     "abort",
				"started_at": taskStartedAt.UTC().Format(time.RFC3339),
				"elapsed":    formatElapsed(time.Since(taskStartedAt)),
			})

			return &RunResult{Status: RunAbort}, nil
		}

		c.setTaskStatus(storageKey, map[string]any{
			"status":     "error",
			"started_at": taskStartedAt.UTC().Format(time.RFC3339),
			"elapsed":    formatElapsed(time.Since(taskStartedAt)),
		})

		return nil, fmt.Errorf("could not get container logs: %w", err)
	}

	emitMissingOutput(stdout.String(), finalStdout.String(), "stdout", streamCallback)
	emitMissingOutput(stderr.String(), finalStderr.String(), "stderr", streamCallback)

	if containerStatus.ExitCode() != 0 {
		logger.Warn("container.run.failed", "exitCode", containerStatus.ExitCode())
	} else {
		logger.Info("container.run.done", "exitCode", containerStatus.ExitCode())
	}

	defer func() {
		err := container.Cleanup(ctx)
		if err != nil {
			logger.Error("container.cleanup.error", "err", err)
		}
	}()

	return c.buildFinalResult(containerStatus, stdout, stderr, finalStdout, finalStderr, logs, storageKey, taskStartedAt, logger)
}

// pollContainerStatus polls the container until it finishes. Returns nil status
// (with nil error) on abort.
func (c *PipelineRunner) pollContainerStatus(
	ctx context.Context,
	container orchestra.Container,
	storageKey string,
	taskStartedAt time.Time,
	cancelStream context.CancelFunc,
) (orchestra.ContainerStatus, error) {
	for {
		containerStatus, err := container.Status(ctx)
		if err != nil {
			cancelStream()

			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				c.setTaskStatus(storageKey, map[string]any{
					"status":     "abort",
					"started_at": taskStartedAt.UTC().Format(time.RFC3339),
					"elapsed":    formatElapsed(time.Since(taskStartedAt)),
				})

				return nil, nil
			}

			c.setTaskStatus(storageKey, map[string]any{
				"status":     "error",
				"started_at": taskStartedAt.UTC().Format(time.RFC3339),
				"elapsed":    formatElapsed(time.Since(taskStartedAt)),
			})

			return nil, fmt.Errorf("could not get container status: %w", err)
		}

		if containerStatus.IsDone() {
			return containerStatus, nil
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// buildFinalResult applies redaction, reconciles logs, and persists the
// completed task status.
func (c *PipelineRunner) buildFinalResult(
	containerStatus orchestra.ContainerStatus,
	stdout, stderr, finalStdout, finalStderr *strings.Builder,
	logs []TaskLogEntry,
	storageKey string,
	taskStartedAt time.Time,
	logger *slog.Logger,
) (*RunResult, error) {
	stdoutStr := preferCompleteOutput(stdout.String(), finalStdout.String())
	stderrStr := preferCompleteOutput(stderr.String(), finalStderr.String())

	if len(c.secretValues) > 0 {
		stdoutStr = support.RedactSecrets(stdoutStr, c.secretValues)
		stderrStr = support.RedactSecrets(stderrStr, c.secretValues)

		for idx := range logs {
			logs[idx].Content = support.RedactSecrets(logs[idx].Content, c.secretValues)
		}
	}

	logs = reconcileTaskLogs(logs, stdoutStr, stderrStr)

	status := "success"
	if containerStatus.ExitCode() != 0 {
		status = "failure"
		logger.Warn("container.run.output", "stdout", stdoutStr, "stderr", stderrStr)
	} else {
		logger.Debug("container.logs", "stdout", stdoutStr, "stderr", stderrStr)
	}

	c.setTaskStatus(storageKey, map[string]any{
		"status":     status,
		"code":       containerStatus.ExitCode(),
		"logs":       logs,
		"started_at": taskStartedAt.UTC().Format(time.RFC3339),
		"elapsed":    formatElapsed(time.Since(taskStartedAt)),
	})

	return &RunResult{
		Status: RunComplete,
		Stdout: stdoutStr,
		Stderr: stderrStr,
		Code:   containerStatus.ExitCode(),
	}, nil
}

// streamLogsWithCallback streams container logs and invokes the callback with each chunk.
func (c *PipelineRunner) streamLogsWithCallback(
	ctx context.Context,
	container orchestra.Container,
	callback OutputCallback,
	stdout, stderr *strings.Builder,
) {
	logger := c.logger

	stdoutPR, stdoutPW := io.Pipe()
	stderrPR, stderrPW := io.Pipe()

	var wg sync.WaitGroup

	// Stream logs from the container into the two pipes.
	wg.Go(func() {
		defer func() { _ = stdoutPW.Close() }()
		defer func() { _ = stderrPW.Close() }()

		err := container.Logs(ctx, stdoutPW, stderrPW, true)
		if err != nil && ctx.Err() == nil {
			logger.Debug("container.streamLogs.error", "err", err)
		}
	})

	// Read stdout chunks and invoke callback.
	wg.Go(func() {
		c.readStreamChunks(ctx, stdoutPR, "stdout", stdout, callback, logger)
	})

	// Read stderr chunks and invoke callback.
	wg.Go(func() {
		c.readStreamChunks(ctx, stderrPR, "stderr", stderr, callback, logger)
	})

	wg.Wait()
}

// readStreamChunks reads from r in 4 KiB chunks, appends to builder,
// and invokes callback for the given stream name.
func (c *PipelineRunner) readStreamChunks(
	ctx context.Context,
	r io.Reader,
	stream string,
	builder *strings.Builder,
	callback OutputCallback,
	logger *slog.Logger,
) {
	buf := make([]byte, 4096)

	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			builder.WriteString(chunk)
			callback(stream, chunk)
		}

		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				logger.Debug("container.streamLogs.read.error", "stream", stream, "err", err)
			}

			break
		}
	}
}

func preferCompleteOutput(streamed, final string) string {
	if len(final) > len(streamed) {
		return final
	}

	return streamed
}

func emitMissingOutput(streamed, final, stream string, callback OutputCallback) {
	if callback == nil || len(final) <= len(streamed) {
		return
	}

	if strings.HasPrefix(final, streamed) {
		callback(stream, final[len(streamed):])

		return
	}

	callback(stream, final)
}

func reconcileTaskLogs(logs []TaskLogEntry, stdout, stderr string) []TaskLogEntry {
	if len(logs) == 0 {
		return buildTaskLogs(stdout, stderr)
	}

	if len(stdout) > len(joinTaskLogs(logs, "stdout")) || len(stderr) > len(joinTaskLogs(logs, "stderr")) {
		return buildTaskLogs(stdout, stderr)
	}

	return logs
}

func buildTaskLogs(stdout, stderr string) []TaskLogEntry {
	logs := make([]TaskLogEntry, 0, 2)
	if stdout != "" {
		logs = append(logs, TaskLogEntry{Type: "stdout", Content: stdout})
	}

	if stderr != "" {
		logs = append(logs, TaskLogEntry{Type: "stderr", Content: stderr})
	}

	return logs
}

func joinTaskLogs(logs []TaskLogEntry, stream string) string {
	var builder strings.Builder

	for _, entry := range logs {
		if entry.Type == stream {
			builder.WriteString(entry.Content)
		}
	}

	return builder.String()
}

// CleanupVolumes cleans up all tracked volumes.
// This triggers cache persistence for CachingVolume wrappers.
func (c *PipelineRunner) CleanupVolumes() error {
	logger := c.logger
	ctx := c.ctx

	var errs []error

	for _, volume := range c.volumes {
		logger.Debug("volume.cleanup", "name", volume.Name())

		err := volume.Cleanup(ctx)
		if err != nil {
			logger.Error("volume.cleanup.error", "name", volume.Name(), "err", err)
			errs = append(errs, err)
		}
	}

	// Clear the slice
	c.volumes = nil

	if len(errs) > 0 {
		return fmt.Errorf("failed to cleanup %d volumes: %w", len(errs), errors.Join(errs...))
	}

	return nil
}

// taskStorageKey returns the storage path for a task within the current run.
// The path follows the pattern /pipeline/{runID}/tasks/{stepID} which the UI
// views (tasks/graph) query via store.GetAll.
func (c *PipelineRunner) taskStorageKey(stepID string) string {
	if c.runID == "" {
		return ""
	}

	return "/pipeline/" + c.runID + "/tasks/" + stepID
}

// formatElapsed returns a human-readable elapsed time string, e.g. "1h 2m 3s".
func formatElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}

	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}

	return fmt.Sprintf("%ds", s)
}

// setTaskStatus persists task status to storage for UI visibility.
// Errors are logged but not propagated — task execution should not fail
// due to status tracking issues.
func (c *PipelineRunner) setTaskStatus(key string, payload map[string]any) {
	if key == "" || c.storage == nil {
		return
	}

	err := c.storage.Set(c.ctx, key, payload)
	if err != nil {
		c.logger.Error("task.status.persist.error", "key", key, "err", err)
	}
}
