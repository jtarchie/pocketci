package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jtarchie/pocketci/orchestra"
)

type Container struct {
	id     string
	client *client.Client
	task   orchestra.Task
}

// ID returns the Docker container ID.
func (d *Container) ID() string {
	return d.id
}

type ContainerStatus struct {
	state *container.State
}

func (d *Container) Status(ctx context.Context) (orchestra.ContainerStatus, error) {
	// doc: https://docs.docker.com/reference/api/engine/version/v1.43/#tag/Container/operation/ContainerInspect
	inspection, err := d.client.ContainerInspect(ctx, d.id)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	return &ContainerStatus{
		state: inspection.State,
	}, nil
}

// Logs retrieves container logs. When follow is false, returns all logs up to now.
// When follow is true, streams logs in real-time until the context is cancelled.
func (d *Container) Logs(ctx context.Context, stdout, stderr io.Writer, follow bool) error {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
	}

	logs, err := d.client.ContainerLogs(ctx, d.id, options)
	if err != nil {
		return fmt.Errorf("failed to get container logs: %w", err)
	}

	if follow {
		defer func() { _ = logs.Close() }()
	}

	_, err = stdcopy.StdCopy(stdout, stderr, logs)
	if err != nil && (follow && ctx.Err() != nil) {
		// Expected error when following and context cancelled
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to copy logs: %w", err)
	}

	return nil
}

func (d *Container) Cleanup(ctx context.Context) error {
	err := d.client.ContainerRemove(ctx, d.id, container.RemoveOptions{
		Force:         true,
		RemoveLinks:   false,
		RemoveVolumes: false,
	})
	if err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	return nil
}

func (s *ContainerStatus) IsDone() bool {
	return s.state.Status == "exited"
}

func (s *ContainerStatus) ExitCode() int {
	return s.state.ExitCode
}

func (d *Docker) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	logger := d.logger.With("taskID", task.ID)

	logger.Debug("image.pull", "image", task.Image)

	reader, err := d.client.ImagePull(ctx, task.Image, image.PullOptions{})
	if err != nil {
		logger.Error("image.pull.initiate", "image", task.Image, "err", err)

		return nil, fmt.Errorf("failed to initiate pull image: %w", err)
	}

	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		logger.Error("image.pull.copy", "image", task.Image, "err", err)

		return nil, fmt.Errorf("failed to pull image: %w", err)
	}

	containerName := fmt.Sprintf("%s-%s", d.namespace, task.ID)

	mounts := []mount.Mount{}

	for _, taskMount := range task.Mounts {
		volume, err := d.CreateVolume(ctx, taskMount.Name, 0)
		if err != nil {
			logger.Error("volume.create.docker.error", "name", taskMount.Name, "err", err)

			return nil, fmt.Errorf("failed to create volume: %w", err)
		}

		dockerVolume, _ := volume.(*Volume)

		mounts = append(mounts, mount.Mount{
			Type:   "volume",
			Source: dockerVolume.volume.Name,
			Target: filepath.Join("/tmp", containerName, taskMount.Path),
		})
	}

	env := []string{}
	for k, v := range task.Env {
		env = append(env, k+"="+v)
	}

	enabledStdin := task.Stdin != nil

	// Set up container resources (CPU and memory limits)
	resources := container.Resources{}
	if task.ContainerLimits.CPU > 0 {
		resources.CPUShares = task.ContainerLimits.CPU
	}
	if task.ContainerLimits.Memory > 0 {
		resources.Memory = task.ContainerLimits.Memory
	}

	workDir := task.WorkDir
	if workDir == "" {
		workDir = filepath.Join("/tmp", containerName)
	}

	response, err := d.client.ContainerCreate(
		ctx,
		&container.Config{
			Image: task.Image,
			Cmd:   task.Command,
			// Override entrypoint to ensure our command runs directly, not as args to image's entrypoint
			Entrypoint: []string{},
			Labels: map[string]string{
				"orchestra.namespace": d.namespace,
			},
			Env:        env,
			WorkingDir: workDir,
			OpenStdin:  enabledStdin,
			StdinOnce:  enabledStdin,
			User:       task.User,
		},
		&container.HostConfig{
			Mounts:     mounts,
			Privileged: task.Privileged,
			Resources:  resources,
		}, nil, nil,
		containerName,
	)
	if err != nil {
		return d.handleContainerCreateError(ctx, err, containerName, task, logger)
	}

	if enabledStdin {
		logger.Debug("container.attach", "name", containerName)

		attachOptions := container.AttachOptions{
			Stream: true,
			Stdin:  true,
		}

		conn, err := d.client.ContainerAttach(ctx, response.ID, attachOptions)
		if err != nil {
			return nil, fmt.Errorf("failed to attach to container: %w", err)
		}
		defer conn.Close()

		_, err = io.Copy(conn.Conn, task.Stdin)
		if err != nil {
			logger.Error("container.stdin", "name", containerName, "err", err)

			return nil, fmt.Errorf("failed to write to container stdin: %w", err)
		}
	}

	err = d.client.ContainerStart(ctx, response.ID, container.StartOptions{})
	if err != nil {
		logger.Error("container.start.error", "name", containerName, "err", err)

		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	return &Container{
		id:     response.ID,
		client: d.client,
		task:   task,
	}, nil
}

// handleContainerCreateError handles a container creation error, recovering from
// conflict errors by finding the existing container.
func (d *Docker) handleContainerCreateError(
	ctx context.Context,
	createErr error,
	containerName string,
	task orchestra.Task,
	logger *slog.Logger,
) (orchestra.Container, error) {
	if !errdefs.IsConflict(createErr) {
		logger.Error("container.create.error", "name", containerName, "err", createErr)

		return nil, fmt.Errorf("failed to create container: %w", createErr)
	}

	logger.Error("container.create.conflict", "name", containerName, "err", createErr, "conflict", true)

	filter := filters.NewArgs()
	filter.Add("name", containerName)

	containers, err := d.client.ContainerList(ctx, container.ListOptions{Filters: filter, All: true})
	if err != nil {
		logger.Error("container.list", "name", containerName, "err", err)

		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	if len(containers) == 0 {
		return nil, fmt.Errorf("failed to find container by name %s: %w", containerName, orchestra.ErrContainerNotFound)
	}

	return &Container{
		id:     containers[0].ID,
		client: d.client,
		task:   task,
	}, nil
}
