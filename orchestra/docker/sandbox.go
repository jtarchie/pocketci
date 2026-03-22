package docker

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jtarchie/pocketci/orchestra"
)

// Sandbox is a long-lived Docker container kept alive with "tail -f /dev/null".
// Commands are dispatched via ContainerExecCreate / ContainerExecAttach.
type Sandbox struct {
	id   string
	d    *Docker
	task orchestra.Task
}

var _ orchestra.Sandbox = (*Sandbox)(nil)

// ID returns the Docker container ID.
func (s *Sandbox) ID() string {
	return s.id
}

// Exec runs cmd inside the sandbox container and streams output to stdout/stderr.
// env and workDir override the sandbox defaults for this single invocation.
func (s *Sandbox) Exec(
	ctx context.Context,
	cmd []string,
	env map[string]string,
	workDir string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (orchestra.ContainerStatus, error) {
	envSlice := make([]string, 0, len(env))
	for k, v := range env {
		envSlice = append(envSlice, k+"="+v)
	}

	if workDir == "" {
		workDir = s.task.WorkDir
	}

	hasStdin := stdin != nil

	execOptions := container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  hasStdin,
		Env:          envSlice,
		WorkingDir:   workDir,
		Cmd:          cmd,
	}

	execResp, err := s.d.client.ContainerExecCreate(ctx, s.id, execOptions)
	if err != nil {
		return nil, fmt.Errorf("exec create failed: %w", err)
	}

	attachResp, err := s.d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach failed: %w", err)
	}
	defer attachResp.Close()

	if hasStdin {
		go func() {
			_, _ = io.Copy(attachResp.Conn, stdin)
			_ = attachResp.CloseWrite()
		}()
	}

	_, err = stdcopy.StdCopy(stdout, stderr, attachResp.Reader)
	if err != nil {
		return nil, fmt.Errorf("exec copy streams failed: %w", err)
	}

	inspect, err := s.d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, fmt.Errorf("exec inspect failed: %w", err)
	}

	return &ContainerStatus{
		state: &container.State{
			Status:   "exited",
			ExitCode: inspect.ExitCode,
		},
	}, nil
}

// Cleanup removes the sandbox container forcibly.
func (s *Sandbox) Cleanup(ctx context.Context) error {
	err := s.d.client.ContainerRemove(ctx, s.id, container.RemoveOptions{Force: true})
	if err != nil {
		return fmt.Errorf("failed to remove sandbox container: %w", err)
	}

	return nil
}

// StartSandbox implements orchestra.SandboxDriver.
// It starts a container running "tail -f /dev/null" and returns a Sandbox handle.
func (d *Docker) StartSandbox(ctx context.Context, task orchestra.Task) (orchestra.Sandbox, error) {
	logger := d.logger.With("taskID", task.ID)

	logger.Debug("sandbox.image.pull", "image", task.Image)

	reader, err := d.client.ImagePull(ctx, task.Image, image.PullOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to pull image: %w", err)
	}

	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to pull image: %w", err)
	}

	containerName := fmt.Sprintf("%s-%s-sandbox", d.namespace, task.ID)

	mounts := []mount.Mount{}
	for _, taskMount := range task.Mounts {
		vol, err := d.CreateVolume(ctx, taskMount.Name, 0)
		if err != nil {
			return nil, fmt.Errorf("sandbox: failed to create volume: %w", err)
		}

		dockerVol, _ := vol.(*Volume)

		mounts = append(mounts, mount.Mount{
			Type:   "volume",
			Source: dockerVol.volume.Name,
			Target: filepath.Join("/tmp", containerName, taskMount.Path),
		})
	}

	env := []string{}
	for k, v := range task.Env {
		env = append(env, k+"="+v)
	}

	workDir := task.WorkDir
	if workDir == "" {
		workDir = filepath.Join("/tmp", containerName)
	}

	resources := container.Resources{}
	if task.ContainerLimits.CPU > 0 {
		resources.CPUShares = task.ContainerLimits.CPU
	}
	if task.ContainerLimits.Memory > 0 {
		resources.Memory = task.ContainerLimits.Memory
	}

	resp, err := d.client.ContainerCreate(
		ctx,
		&container.Config{
			Image:      task.Image,
			Cmd:        []string{"tail", "-f", "/dev/null"},
			Entrypoint: []string{},
			Env:        env,
			WorkingDir: workDir,
			User:       task.User,
			Labels: map[string]string{
				"orchestra.namespace": d.namespace,
			},
		},
		&container.HostConfig{
			Mounts:     mounts,
			Privileged: task.Privileged,
			Resources:  resources,
		},
		nil, nil,
		containerName,
	)
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to create container: %w", err)
	}

	err = d.client.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox: failed to start container: %w", err)
	}

	logger.Debug("sandbox.started", "containerID", resp.ID)

	return &Sandbox{
		id:   resp.ID,
		d:    d,
		task: task,
	}, nil
}
