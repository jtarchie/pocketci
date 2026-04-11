package backwards

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/storage"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// TaskHandler executes task steps by running containers.
type TaskHandler struct{}

func (h *TaskHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	if step.Parallelism > 1 {
		return h.executeParallel(sc, step, pathPrefix)
	}

	taskName := step.Task

	sc.ExecutedTasksMu.Lock()
	if !sc.PreRegisteredTasks[taskName] {
		sc.ExecutedTasks = append(sc.ExecutedTasks, taskName)
	}
	sc.ExecutedTasksMu.Unlock()

	var env map[string]string
	if step.TaskConfig != nil {
		env = step.TaskConfig.Env
	}

	return h.runTask(sc, step, pathPrefix, taskName, env)
}

func (h *TaskHandler) executeParallel(sc *StepContext, step *config.Step, pathPrefix string) error {
	count := step.Parallelism
	limit := resolveLimit(sc, 0, count)

	// Pre-populate ExecutedTasks for deterministic assertion order.
	for i := 1; i <= count; i++ {
		sc.appendExecutedTask(fmt.Sprintf("%s-%d", step.Task, i))
	}

	g, _ := errgroup.WithContext(sc.Ctx)
	sem := semaphore.NewWeighted(int64(limit))

	for i := 1; i <= count; i++ {
		index := i

		err := sem.Acquire(sc.Ctx, 1)
		if err != nil {
			break
		}

		g.Go(func() error {
			defer sem.Release(1)

			indexedName := fmt.Sprintf("%s-%d", step.Task, index)
			env := cloneEnv(step.TaskConfig.Env)
			env["CI_TASK_INDEX"] = strconv.Itoa(index)
			env["CI_TASK_COUNT"] = strconv.Itoa(count)

			return h.runTask(sc, step, pathPrefix, indexedName, env)
		})
	}

	err := g.Wait()
	if err != nil {
		return fmt.Errorf("task steps: %w", err)
	}

	return nil
}

func (h *TaskHandler) loadRunTaskConfig(sc *StepContext, step *config.Step, pathPrefix, taskName string, env map[string]string) (*config.TaskConfig, map[string]string, error) {
	taskConfig := step.TaskConfig

	if step.File != "" {
		loaded, err := trackLoadFile(sc, step.File, pathPrefix)
		if err != nil {
			return nil, nil, &TaskErroredError{TaskName: taskName, Err: err}
		}

		taskConfig = loaded
	} else if step.URI != "" {
		loaded, err := trackLoadURI(sc, step.URI, pathPrefix)
		if err != nil {
			return nil, nil, &TaskErroredError{TaskName: taskName, Err: err}
		}

		taskConfig = loaded
	}

	if env == nil && taskConfig != nil {
		env = mergeJobParams(sc.JobParams, taskConfig.Env)
	}

	return taskConfig, env, nil
}

// runTask executes a single container task with the given name and environment.
func (h *TaskHandler) runTask(sc *StepContext, step *config.Step, pathPrefix, taskName string, env map[string]string) error {
	taskConfig, env, err := h.loadRunTaskConfig(sc, step, pathPrefix, taskName, env)
	if err != nil {
		return err
	}

	storageKey := fmt.Sprintf("%s/%s/tasks/%s", sc.BaseStorageKey(), pathPrefix, taskName)

	startedAt := time.Now()

	err = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "pending",
		"started_at": startedAt.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	mounts := resolveMounts(sc, taskConfig, taskName)

	task := orchestra.Task{
		ID:         fmt.Sprintf("%s-%s", sc.JobName, taskName),
		Command:    buildCommand(taskConfig),
		Env:        env,
		Image:      resolveImage(taskConfig),
		Mounts:     mounts,
		Privileged: step.Privileged,
		ContainerLimits: orchestra.ContainerLimits{
			CPU:    taskConfig.ContainerLimits.CPU,
			Memory: taskConfig.ContainerLimits.Memory,
		},
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

	var stdout, stderr bytes.Buffer

	err = container.Logs(sc.Ctx, &stdout, &stderr, false)
	if err != nil {
		sc.Logger.Error("task.logs.error", "task", taskName, "err", err)
	}

	logs := make([]any, 0, 2)
	if stdout.Len() > 0 {
		logs = append(logs, map[string]string{"type": "stdout", "content": stdout.String()})
	}

	if stderr.Len() > 0 {
		logs = append(logs, map[string]string{"type": "stderr", "content": stderr.String()})
	}

	if sc.OutputCallback != nil {
		if stdout.Len() > 0 {
			sc.OutputCallback("stdout", stdout.String())
		}

		if stderr.Len() > 0 {
			sc.OutputCallback("stderr", stderr.String())
		}
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
		"logs":       logs,
	})
	if err != nil {
		return fmt.Errorf("storage set result: %w", err)
	}

	err = checkTaskAssertions(step, taskName, exitCode, stdout.String(), stderr.String())
	if err != nil {
		return err
	}

	if exitCode != 0 {
		return &TaskFailedError{TaskName: taskName, Code: exitCode}
	}

	return nil
}

func checkTaskAssertions(step *config.Step, taskName string, exitCode int, stdout, stderr string) error {
	if step.Assert == nil {
		return nil
	}

	if step.Assert.Code != nil && exitCode != *step.Assert.Code {
		return &AssertionError{
			Message: fmt.Sprintf("task %q: expected exit code %d, got %d", taskName, *step.Assert.Code, exitCode),
		}
	}

	if step.Assert.Stdout != "" && !strings.Contains(stdout, step.Assert.Stdout) {
		return &AssertionError{
			Message: fmt.Sprintf("task %q: stdout does not contain %q", taskName, step.Assert.Stdout),
		}
	}

	if step.Assert.Stderr != "" && !strings.Contains(stderr, step.Assert.Stderr) {
		return &AssertionError{
			Message: fmt.Sprintf("task %q: stderr does not contain %q", taskName, step.Assert.Stderr),
		}
	}

	return nil
}

// mergeJobParams returns a new env map with jobParams as the base layer
// and stepEnv as the override layer. Step env takes precedence.
func mergeJobParams(jobParams, stepEnv map[string]string) map[string]string {
	if len(jobParams) == 0 {
		return stepEnv
	}

	if len(stepEnv) == 0 {
		return jobParams
	}

	merged := make(map[string]string, len(jobParams)+len(stepEnv))
	for k, v := range jobParams {
		merged[k] = v
	}

	for k, v := range stepEnv {
		merged[k] = v // step env overrides job params
	}

	return merged
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

	cmd := make([]string, 0, 1+len(cfg.Run.Args))
	cmd = append(cmd, cfg.Run.Path)
	cmd = append(cmd, cfg.Run.Args...)

	return cmd
}

// resolveMounts combines cache mounts and input/output mounts for a task.
func resolveMounts(sc *StepContext, cfg *config.TaskConfig, taskName string) orchestra.Mounts {
	cacheMounts := resolveCaches(sc, cfg, taskName)
	ioMounts := resolveInputsOutputs(sc, cfg)

	if len(cacheMounts) == 0 {
		return ioMounts
	}

	if len(ioMounts) == 0 {
		return cacheMounts
	}

	mounts := make(orchestra.Mounts, 0, len(cacheMounts)+len(ioMounts))
	mounts = append(mounts, cacheMounts...)
	mounts = append(mounts, ioMounts...)

	return mounts
}

// resolveInputsOutputs converts TaskConfig inputs and outputs into orchestra.Mounts.
// Volume names are stable per mount name within a job run, so outputs from one
// step become available as inputs to later steps.
func resolveInputsOutputs(sc *StepContext, cfg *config.TaskConfig) orchestra.Mounts {
	if cfg == nil {
		return nil
	}

	totalMounts := len(cfg.Outputs) + len(cfg.Inputs)
	if totalMounts == 0 {
		return nil
	}

	mounts := make(orchestra.Mounts, 0, totalMounts)

	for _, output := range cfg.Outputs {
		volName, ok := sc.KnownVolumes[output.Name]
		if !ok {
			volName = resourceVolumeName(sc.RunID, output.Name)
			sc.KnownVolumes[output.Name] = volName
		}

		mounts = append(mounts, orchestra.Mount{
			Name: volName,
			Path: output.Name,
		})
	}

	for _, input := range cfg.Inputs {
		volName, ok := sc.KnownVolumes[input.Name]
		if !ok {
			volName = resourceVolumeName(sc.RunID, input.Name)
			sc.KnownVolumes[input.Name] = volName
		}

		mounts = append(mounts, orchestra.Mount{
			Name: volName,
			Path: input.Name,
		})
	}

	return mounts
}

// resolveCaches converts TaskConfig.Caches into orchestra.Mounts.
// Volume names are stable per cache path within a job run so multiple tasks
// sharing the same cache path reuse the same volume. Volumes are explicitly
// created via Driver.CreateVolume so the cache layer (if configured) can
// restore from S3 before the container runs. Cleanup of cache volumes is
// deferred to job end via sc.CacheVolumeObjects, not per-task.
//
// When a cache entry has Scope=="task", the volume gets a per-task key prefix
// so different tasks never share cached data even for the same path.
func resolveCaches(sc *StepContext, cfg *config.TaskConfig, taskName string) orchestra.Mounts {
	if cfg == nil || len(cfg.Caches) == 0 {
		return nil
	}

	mounts := make(orchestra.Mounts, 0, len(cfg.Caches))

	for _, cacheEntry := range cfg.Caches {
		// For task-scoped caches the lookup key includes the task name so
		// each task gets its own volume (and therefore its own cache entry).
		lookupKey := cacheEntry.Path
		if cacheEntry.Scope == "task" {
			lookupKey = taskName + "/" + cacheEntry.Path
		}

		volName, ok := sc.CacheVolumes[lookupKey]
		if !ok {
			// Volume name must be stable across runs so the cache layer can
			// restore it from S3. For task-scoped caches the volume name also
			// includes the task name so different tasks get separate physical
			// volumes (and therefore separate on-disk state within a run).
			if cacheEntry.Scope == "task" {
				volName = "cache-" + sanitizeCachePath(taskName) + "-" + sanitizeCachePath(cacheEntry.Path)
			} else {
				volName = "cache-" + sanitizeCachePath(cacheEntry.Path)
			}
			sc.CacheVolumes[lookupKey] = volName

			// For task-scoped caches, augment the driver so the cache key
			// includes the task name segment.
			driver := sc.Driver
			if cacheEntry.Scope == "task" {
				driver = cache.AugmentKeyPrefix(sc.Driver, sanitizeCachePath(taskName))
			}

			// Explicitly create the volume so that the cache driver wrapper
			// can intercept the call and restore from S3 before execution.
			vol, err := driver.CreateVolume(sc.Ctx, volName, 0)
			if err != nil {
				sc.Logger.Warn("cache.volume.create.failed", "path", cacheEntry.Path, "volume", volName, "err", err)
			} else {
				sc.CacheVolumeObjects[lookupKey] = vol
			}
		}

		mounts = append(mounts, orchestra.Mount{
			Name: volName,
			Path: cacheEntry.Path,
		})
	}

	return mounts
}

// sanitizeCachePath converts a cache path to a safe volume name component.
func sanitizeCachePath(path string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(path) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}

	return strings.Trim(b.String(), "-")
}

// loadTaskConfigFromVolume reads a YAML task config from a volume.
// The filePath format is "mount-name/relative/path/to/file.yml".
// loadRawBytesFromVolume reads raw bytes from a file inside a mounted volume.
func loadRawBytesFromVolume(sc *StepContext, filePath string) (data []byte, retErr error) {
	parts := strings.SplitN(filePath, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid file path %q: expected mount-name/path", filePath)
	}

	mountName := parts[0]
	relativePath := parts[1]

	volName, ok := sc.KnownVolumes[mountName]
	if !ok {
		return nil, fmt.Errorf("volume %q not found in known volumes", mountName)
	}

	accessor, ok := sc.Driver.(cache.VolumeDataAccessor)
	if !ok {
		return nil, fmt.Errorf("driver %q does not support reading files from volumes", sc.Driver.Name())
	}

	tarReader, err := accessor.ReadFilesFromVolume(sc.Ctx, volName, relativePath)
	if err != nil {
		return nil, fmt.Errorf("reading file %q from volume %q: %w", relativePath, mountName, err)
	}

	defer func() {
		closeErr := tarReader.Close()
		if closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	content, err := extractFileFromTar(tarReader, relativePath)
	if err != nil {
		return nil, fmt.Errorf("extracting file %q: %w", relativePath, err)
	}

	return content, nil
}

func loadTaskConfigFromVolume(sc *StepContext, filePath string) (*config.TaskConfig, error) {
	content, err := loadRawBytesFromVolume(sc, filePath)
	if err != nil {
		return nil, err
	}

	var taskConfig config.TaskConfig

	unmarshalErr := yaml.UnmarshalWithOptions(content, &taskConfig, yaml.Strict())
	if unmarshalErr != nil {
		return nil, fmt.Errorf("parsing task config from %q: %w", filePath, unmarshalErr)
	}

	return &taskConfig, nil
}

// extractFileFromTar reads the first matching file from a tar archive.
func extractFileFromTar(reader io.Reader, targetPath string) ([]byte, error) {
	tr := tar.NewReader(reader)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("file %q not found in tar archive", targetPath)
		}

		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}

		if header.Name == targetPath || strings.TrimPrefix(header.Name, "./") == targetPath {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("reading file content: %w", err)
			}

			return data, nil
		}
	}
}

type uriScheme int

const (
	schemeFile uriScheme = iota
	schemeHTTP
)

// parseURI classifies a URI by scheme and returns the relevant path/URL.
// For file:// URIs it returns the volume path (with ".." validation).
// For http:// and https:// it returns the full URI unchanged.
func parseURI(uri string) (uriScheme, string, error) {
	if strings.HasPrefix(uri, "file://") {
		path := strings.TrimPrefix(uri, "file://")
		if strings.Contains(path, "..") {
			return 0, "", fmt.Errorf("file:// URI must not contain \"..\" path segments: %q", uri)
		}

		return schemeFile, path, nil
	}

	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return schemeHTTP, uri, nil
	}

	return 0, "", fmt.Errorf("unsupported URI scheme in %q; supported: file://, http://, https://", uri)
}

// loadTaskConfigFromHTTP fetches a task config YAML from an HTTP(S) URL.
func loadTaskConfigFromHTTP(ctx context.Context, uri string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request for %s: %w", uri, err)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", uri, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, uri)
	}

	const maxBodySize = 10 << 20 // 10MB

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", uri, err)
	}

	return data, nil
}

// trackLoadFile wraps loadTaskConfigFromVolume with storage status tracking.
func trackLoadFile(sc *StepContext, filePath, pathPrefix string) (*config.TaskConfig, error) {
	mountName := strings.SplitN(filePath, "/", 2)[0]
	storageKey := fmt.Sprintf("%s/%s/load-file", sc.BaseStorageKey(), pathPrefix)
	startedAt := time.Now()

	_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "pending",
		"file":       filePath,
		"volume":     mountName,
		"started_at": startedAt.Format(time.RFC3339),
	})

	loaded, err := loadTaskConfigFromVolume(sc, filePath)
	elapsed := time.Since(startedAt)

	if err != nil {
		errMsg := err.Error()

		_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
			"status":       "failure",
			"file":         filePath,
			"volume":       mountName,
			"started_at":   startedAt.Format(time.RFC3339),
			"elapsed":      elapsed.String(),
			"errorMessage": errMsg,
			"logs":         []any{map[string]string{"type": "stderr", "content": errMsg}},
		})

		return nil, err
	}

	_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "success",
		"file":       filePath,
		"volume":     mountName,
		"started_at": startedAt.Format(time.RFC3339),
		"elapsed":    elapsed.String(),
		"logs":       []any{map[string]string{"type": "stdout", "content": fmt.Sprintf("loaded %s from volume %s", filePath, mountName)}},
	})

	return loaded, nil
}

// trackLoadURI wraps URI loading with storage status tracking.
// For file:// URIs it delegates to trackLoadFile.
// For http(s):// URIs it fetches remotely and parses YAML.
func trackLoadURI(sc *StepContext, uri, pathPrefix string) (*config.TaskConfig, error) {
	scheme, value, err := parseURI(uri)
	if err != nil {
		return nil, err
	}

	if scheme == schemeFile {
		return trackLoadFile(sc, value, pathPrefix)
	}

	storageKey := fmt.Sprintf("%s/%s/load-uri", sc.BaseStorageKey(), pathPrefix)
	startedAt := time.Now()

	_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "pending",
		"uri":        uri,
		"started_at": startedAt.Format(time.RFC3339),
	})

	data, err := loadTaskConfigFromHTTP(sc.Ctx, uri)
	elapsed := time.Since(startedAt)

	if err != nil {
		errMsg := err.Error()

		_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
			"status":       "failure",
			"uri":          uri,
			"started_at":   startedAt.Format(time.RFC3339),
			"elapsed":      elapsed.String(),
			"errorMessage": errMsg,
			"logs":         []any{map[string]string{"type": "stderr", "content": errMsg}},
		})

		return nil, err
	}

	var taskConfig config.TaskConfig

	unmarshalErr2 := yaml.UnmarshalWithOptions(data, &taskConfig, yaml.Strict())
	if unmarshalErr2 != nil {
		errMsg := fmt.Sprintf("parsing task config from %s: %s", uri, unmarshalErr2.Error())

		_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
			"status":       "failure",
			"uri":          uri,
			"started_at":   startedAt.Format(time.RFC3339),
			"elapsed":      elapsed.String(),
			"errorMessage": errMsg,
			"logs":         []any{map[string]string{"type": "stderr", "content": errMsg}},
		})

		return nil, fmt.Errorf("parsing task config from %q: %w", uri, unmarshalErr2)
	}

	_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "success",
		"uri":        uri,
		"started_at": startedAt.Format(time.RFC3339),
		"elapsed":    elapsed.String(),
		"logs":       []any{map[string]string{"type": "stdout", "content": "loaded config from " + uri}},
	})

	return &taskConfig, nil
}
