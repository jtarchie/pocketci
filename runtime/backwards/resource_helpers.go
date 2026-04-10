package backwards

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	config "github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/resources"
	"github.com/samber/lo"
)

// findResource looks up a resource by name from the pipeline config.
func findResource(rs config.Resources, name string) (*config.Resource, error) {
	r, ok := lo.Find(rs, func(r config.Resource) bool { return r.Name == name })
	if !ok {
		return nil, fmt.Errorf("resource %q not found", name)
	}

	return &r, nil
}

// findResourceType looks up a resource type by name from the pipeline config.
func findResourceType(rts config.ResourceTypes, typeName string) (*config.ResourceType, error) {
	rt, ok := lo.Find(rts, func(rt config.ResourceType) bool { return rt.Name == typeName })
	if !ok {
		return nil, fmt.Errorf("resource type %q not found", typeName)
	}

	return &rt, nil
}

// paramsToAnyMap converts map[string]string to map[string]any for the resources API.
func paramsToAnyMap(params map[string]string) map[string]any {
	if params == nil {
		return nil
	}

	m := make(map[string]any, len(params))
	for k, v := range params {
		m[k] = v
	}

	return m
}

// resolveLimit determines the effective concurrency limit.
// Priority: job MaxInFlight > step limit > step count (unlimited).
func resolveLimit(sc *StepContext, stepLimit, stepCount int) int {
	if sc.MaxInFlight > 0 {
		return sc.MaxInFlight
	}

	if stepLimit > 0 {
		return stepLimit
	}

	return stepCount
}

// resourceVolumeName returns the driver volume name for a resource mount.
func resourceVolumeName(runID, name string) string {
	return fmt.Sprintf("vol-%s-%s", runID, name)
}

// getScopedResourceName returns a scoped name for resource version storage.
// pipelineID is used to scope versions per-pipeline, matching the JS runtime's
// behaviour of `${pipelineID}/${resourceName}`.
func getScopedResourceName(pipelineID, resourceName string) string {
	if pipelineID == "" {
		pipelineID = "default"
	}

	return pipelineID + "/" + resourceName
}

// runResourceContainer runs a container task for resource operations (check, in, out).
// It returns the container's stdout and any error.
func runResourceContainer(sc *StepContext, taskName, image string, command []string, mounts orchestra.Mounts, stdinData []byte) (string, error) {
	task := orchestra.Task{
		ID:      fmt.Sprintf("%s-%s", sc.JobName, taskName),
		Command: command,
		Image:   image,
		Mounts:  mounts,
		Stdin:   bytes.NewReader(stdinData),
	}

	container, err := sc.Driver.RunContainer(sc.Ctx, task)
	if err != nil {
		return "", fmt.Errorf("run container %q: %w", taskName, err)
	}

	defer func() { _ = container.Cleanup(sc.Ctx) }()

	status, err := waitForContainer(sc.Ctx, container)
	if err != nil {
		return "", fmt.Errorf("wait for container %q: %w", taskName, err)
	}

	var stdout, stderr bytes.Buffer

	err = container.Logs(sc.Ctx, &stdout, &stderr, false)
	if err != nil {
		sc.Logger.Error("resource.container.logs.error", "task", taskName, "err", err)
	}

	if status.ExitCode() != 0 {
		return "", fmt.Errorf("container %q exited with code %d: %s", taskName, status.ExitCode(), stderr.String())
	}

	return stdout.String(), nil
}

// waitForContainer polls a container for completion, respecting context cancellation.
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

// fetchNativeResource fetches a resource version using the native resource API.
// params is nil for put's implicit get (no step params needed).
func fetchNativeResource(sc *StepContext, resource *config.Resource, version map[string]string, params map[string]string) error {
	volName := resourceVolumeName(sc.RunID, resource.Name)

	vol, err := sc.Driver.CreateVolume(sc.Ctx, volName, 0)
	if err != nil {
		return fmt.Errorf("create volume for %q: %w", resource.Name, err)
	}

	sc.KnownVolumes[resource.Name] = volName

	res, err := resources.Get(resource.Type)
	if err != nil {
		return fmt.Errorf("get native resource %q: %w", resource.Type, err)
	}

	_, err = res.In(sc.Ctx, &nativeVolumeContext{vol: vol, driver: sc.Driver}, resources.InRequest{
		Source:  resource.Source,
		Version: version,
		Params:  paramsToAnyMap(params),
	})
	if err != nil {
		return fmt.Errorf("native fetch %q: %w", resource.Name, err)
	}

	sc.appendExecutedTask("get-" + resource.Name)

	return nil
}

// fetchContainerResource fetches a resource version using a resource container (/opt/resource/in).
func fetchContainerResource(sc *StepContext, resource *config.Resource, resourceType *config.ResourceType, version map[string]string, resourceName, pathPrefix string) error {
	image, _ := resourceType.Source["repository"].(string)
	volName := resourceVolumeName(sc.RunID, resourceName)
	sc.KnownVolumes[resourceName] = volName

	mounts := orchestra.Mounts{
		{Name: volName, Path: resourceName},
	}

	stdinData, err := resourceStdinJSON(map[string]any{
		"source":  resource.Source,
		"version": version,
	})
	if err != nil {
		return err
	}

	taskName := fmt.Sprintf("get-%s-%s", resourceName, pathPrefix)

	_, err = runResourceContainer(sc, taskName, image, []string{"/opt/resource/in", "./" + resourceName}, mounts, stdinData)
	if err != nil {
		return fmt.Errorf("container fetch %q: %w", resourceName, err)
	}

	sc.appendExecutedTask("get-" + resourceName)

	return nil
}

// resourceStdinJSON builds the JSON stdin payload for a resource container operation.
func resourceStdinJSON(fields map[string]any) ([]byte, error) {
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("marshal resource stdin: %w", err)
	}

	return data, nil
}

// nativeVolumeContext implements resources.VolumeContext for the native driver.
// File I/O goes directly to the host filesystem; sandboxes are started via SandboxDriver.
type nativeVolumeContext struct {
	vol    orchestra.Volume
	driver orchestra.Driver
}

func (v *nativeVolumeContext) WriteFile(ctx context.Context, path string, data []byte) error {
	if accessor, ok := v.driver.(cache.VolumeDataAccessor); ok {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)

		err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o600, Size: int64(len(data))})
		if err != nil {
			return fmt.Errorf("write tar header for %q: %w", path, err)
		}

		_, err = tw.Write(data)
		if err != nil {
			return fmt.Errorf("write tar data for %q: %w", path, err)
		}

		err = tw.Close()
		if err != nil {
			return fmt.Errorf("close tar for %q: %w", path, err)
		}

		err = accessor.CopyToVolume(ctx, v.vol.Name(), &buf)
		if err != nil {
			return fmt.Errorf("copy to volume for %q: %w", path, err)
		}

		return nil
	}

	err := os.WriteFile(filepath.Join(v.vol.Path(), path), data, 0o600)
	if err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}

	return nil
}

func (v *nativeVolumeContext) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if accessor, ok := v.driver.(cache.VolumeDataAccessor); ok {
		rc, err := accessor.ReadFilesFromVolume(ctx, v.vol.Name(), path)
		if err != nil {
			return nil, fmt.Errorf("read from volume for %q: %w", path, err)
		}
		defer func() { _ = rc.Close() }()

		tr := tar.NewReader(rc)
		_, err = tr.Next()
		if err != nil {
			return nil, fmt.Errorf("read tar entry for %q: %w", path, err)
		}

		fileData, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read tar data for %q: %w", path, err)
		}

		return fileData, nil
	}

	data, err := os.ReadFile(filepath.Join(v.vol.Path(), path))
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}

	return data, nil
}

func (v *nativeVolumeContext) OpenSandbox(ctx context.Context, image, mountPath string) (resources.Sandbox, error) {
	sd, ok := v.driver.(orchestra.SandboxDriver)
	if !ok {
		return nil, fmt.Errorf("driver %q does not support sandboxes", v.driver.Name())
	}

	sandbox, err := sd.StartSandbox(ctx, orchestra.Task{
		Image:  image,
		Mounts: orchestra.Mounts{{Name: v.vol.Name(), Path: mountPath}},
	})
	if err != nil {
		return nil, fmt.Errorf("open sandbox: %w", err)
	}

	return &sandboxAdapter{inner: sandbox}, nil
}

// sandboxAdapter wraps orchestra.Sandbox, converting ContainerStatus returns to errors.
type sandboxAdapter struct{ inner orchestra.Sandbox }

func (s *sandboxAdapter) Exec(ctx context.Context, cmd []string, env map[string]string, workDir string, stdin io.Reader, stdout, stderr io.Writer) error {
	status, err := s.inner.Exec(ctx, cmd, env, workDir, stdin, stdout, stderr)
	if err != nil {
		return fmt.Errorf("sandbox exec: %w", err)
	}

	if status.ExitCode() != 0 {
		return fmt.Errorf("command exited with code %d", status.ExitCode())
	}

	return nil
}

func (s *sandboxAdapter) Close(ctx context.Context) error {
	err := s.inner.Cleanup(ctx)
	if err != nil {
		return fmt.Errorf("sandbox cleanup: %w", err)
	}

	return nil
}
