package backwards

import (
	"errors"
	"fmt"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/runtime/runner"
	"github.com/jtarchie/pocketci/storage"
)

// BuildImageHandler executes a build_image step by invoking the moby/buildkit
// image through the pipeline runner.
type BuildImageHandler struct{}

func (h *BuildImageHandler) Execute(sc *StepContext, step *config.Step, pathPrefix string) error {
	cfg := step.BuildImage
	if cfg == nil {
		return errors.New("build_image: missing config")
	}

	if cfg.Tag == "" {
		return &TaskErroredError{TaskName: "build_image", Err: errors.New("tag is required")}
	}

	if cfg.Context == "" {
		return &TaskErroredError{TaskName: "build_image", Err: errors.New("context is required")}
	}

	name := buildImageStepName(cfg)

	sc.appendExecutedTask(name)

	pseudoCfg := &config.TaskConfig{
		Inputs: cfg.Inputs,
		Caches: cfg.Caches,
	}

	// Resolve cache mounts (which fires cache_restore tasks) before writing
	// the build_image "pending" row, so the restore's storage id sorts
	// before the build_image row and the /tasks tree renders them in the
	// order they actually run.
	inputMounts := resolveInputsOutputs(sc, pseudoCfg)
	cacheMounts := resolveCaches(sc, pseudoCfg, name, pathPrefix)

	storageKey := fmt.Sprintf("%s/%s/build_image/%s", sc.BaseStorageKey(), pathPrefix, name)
	startedAt := time.Now()

	err := sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{
		"status":     "pending",
		"started_at": startedAt.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("storage set pending: %w", err)
	}

	env, err := resolveEnvSecrets(sc, name, cfg.Env)
	if err != nil {
		_ = sc.Storage.Set(sc.Ctx, storageKey, storage.Payload{"status": "failure"})

		return &TaskErroredError{TaskName: name, Err: err}
	}

	registryAuth := buildImageRegistryFromConfig(cfg.Registry)

	limits := cfg.EffectiveLimits()
	input := runner.BuildImageInput{
		Name:         name,
		Context:      cfg.Context,
		Dockerfile:   cfg.Dockerfile,
		Tag:          cfg.Tag,
		Push:         cfg.Push,
		BuildArgs:    cfg.BuildArgs,
		Target:       cfg.Target,
		Platforms:    cfg.Platforms,
		Image:        cfg.Image,
		Inputs:       mountsToVolumeResults(inputMounts),
		Caches:       mountsToVolumeResults(cacheMounts),
		Env:          env,
		RegistryAuth: registryAuth,
		StorageKey:   storageKey,
		Limits: runner.BuildImageLimits{
			CPU:     limits.CPU,
			CPUKind: limits.CPUKind,
			Memory:  int64(limits.Memory),
		},
	}

	if cfg.Timeout > 0 {
		input.Timeout = cfg.Timeout.String()
	}

	if sc.OutputCallback != nil {
		input.OnOutput = sc.OutputCallback
	}

	result, runErr := runner.BuildImage(sc.PipelineRunner, input)
	elapsed := time.Since(startedAt)

	resultStatus := "success"
	if runErr != nil {
		resultStatus = "failure"
	}

	payload := storage.Payload{
		"status":     resultStatus,
		"started_at": startedAt.Format(time.RFC3339),
		"elapsed":    elapsed.String(),
		"tag":        cfg.Tag,
	}

	if result != nil {
		payload["digest"] = result.Digest
	}

	_ = sc.Storage.Set(sc.Ctx, storageKey, payload)

	if runErr != nil {
		return &TaskFailedError{TaskName: name, Code: 1}
	}

	return nil
}

// buildImageStepName returns a stable identifier for a build_image step.
// Falls back to a sanitized form of the tag when no explicit Name is set.
func buildImageStepName(cfg *config.BuildImageConfig) string {
	if cfg == nil {
		return "build_image"
	}

	if cfg.Name != "" {
		return cfg.Name
	}

	return sanitizeCachePath(cfg.Tag)
}

// buildImageRegistryFromConfig copies the YAML config into the runner type.
// Secret resolution happens later in PipelineRunner.Run via injectSecrets so
// the runner can also track resolved values for log redaction.
func buildImageRegistryFromConfig(reg *config.BuildImageRegistry) *runner.BuildImageRegistryAuth {
	if reg == nil {
		return nil
	}

	return &runner.BuildImageRegistryAuth{
		Registry: reg.Hostname,
		Username: reg.Username,
		Password: reg.Password,
		Insecure: reg.Insecure,
	}
}

// mountsToVolumeResults converts orchestra.Mounts into the map shape expected
// by runner.BuildImageInput.Inputs/Caches. The map key is the mount path; the
// value is a VolumeResult carrying just the volume name (the runner forwards
// it to the orchestra driver, which materializes the volume on demand).
func mountsToVolumeResults(mounts orchestra.Mounts) map[string]runner.VolumeResult {
	if len(mounts) == 0 {
		return nil
	}

	result := make(map[string]runner.VolumeResult, len(mounts))
	for _, m := range mounts {
		result[m.Path] = runner.VolumeResult{Name: m.Name, Path: m.Path}
	}

	return result
}
