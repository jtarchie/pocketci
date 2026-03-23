package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jtarchie/pocketci/cache"
)

// pullImage ensures an image is available locally, waiting for the pull to complete.
func (d *Docker) pullImage(ctx context.Context, imageName string) error {
	reader, err := d.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	// Drain the reader to wait for pull completion
	_, _ = io.Copy(io.Discard, reader)
	return nil
}

const cacheHelperImage = "busybox:latest"

// CopyToVolume implements cache.VolumeDataAccessor.
// Creates a temporary container to extract tar data into the volume.
func (d *Docker) CopyToVolume(ctx context.Context, volumeName string, reader io.Reader) error {
	fullVolumeName := fmt.Sprintf("%s-%s", d.namespace, volumeName)

	// Ensure busybox image is available
	if err := d.pullImage(ctx, cacheHelperImage); err != nil {
		// Try to continue anyway, image might already exist
		d.logger.Debug("cache.helper.pull.copyto.failed", "error", err)
	}

	// Create a temporary container with the volume mounted
	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: cacheHelperImage,
			Cmd:   []string{"sh", "-c", "cat > /dev/null"}, // Just consume stdin
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Source: fullVolumeName,
					Target: "/volume",
				},
			},
		},
		nil, nil, "",
	)
	if err != nil {
		return fmt.Errorf("failed to create cache helper container: %w", err)
	}

	defer func() {
		_ = d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	}()

	// Copy tar data to the volume mount path
	err = d.client.CopyToContainer(ctx, resp.ID, "/volume", reader, container.CopyToContainerOptions{})
	if err != nil {
		return fmt.Errorf("failed to copy data to volume: %w", err)
	}

	return nil
}

// CopyFromVolume implements cache.VolumeDataAccessor.
// Creates a temporary container to read tar data from the volume.
func (d *Docker) CopyFromVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	fullVolumeName := fmt.Sprintf("%s-%s", d.namespace, volumeName)

	// Ensure busybox image is available
	if err := d.pullImage(ctx, cacheHelperImage); err != nil {
		// Try to continue anyway, image might already exist
		d.logger.Debug("cache.helper.pull.copyfrom.failed", "error", err)
	}

	// Create a temporary container with the volume mounted
	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: cacheHelperImage,
			Cmd:   []string{"sleep", "infinity"},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Source: fullVolumeName,
					Target: "/volume",
				},
			},
		},
		nil, nil, "",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache helper container: %w", err)
	}

	// Start the container so we can copy from it
	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

		return nil, fmt.Errorf("failed to start cache helper container: %w", err)
	}

	// Copy tar data from the volume mount path
	reader, _, err := d.client.CopyFromContainer(ctx, resp.ID, "/volume/.")
	if err != nil {
		_ = d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

		return nil, fmt.Errorf("failed to copy data from volume: %w", err)
	}

	// Return a wrapper that cleans up the container when closed
	return &dockerCopyReader{
		ReadCloser:  reader,
		containerID: resp.ID,
		client:      d.client,
	}, nil
}

type dockerCopyReader struct {
	io.ReadCloser
	containerID string
	client      *client.Client
}

func (r *dockerCopyReader) Close() error {
	err := r.ReadCloser.Close()
	// Use Background context so cleanup succeeds even if the original context was cancelled.
	_ = r.client.ContainerRemove(context.Background(), r.containerID, container.RemoveOptions{Force: true})

	return err
}

// ReadFilesFromVolume implements cache.VolumeDataAccessor.
// Creates a temporary container and execs tar to stream specific files from the volume.
func (d *Docker) ReadFilesFromVolume(ctx context.Context, volumeName string, filePaths ...string) (io.ReadCloser, error) {
	fullVolumeName := fmt.Sprintf("%s-%s", d.namespace, volumeName)

	if err := d.pullImage(ctx, cacheHelperImage); err != nil {
		d.logger.Debug("cache.helper.pull.readfiles.failed", "error", err)
	}

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image: cacheHelperImage,
			Cmd:   []string{"sleep", "infinity"},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Source: fullVolumeName,
					Target: "/volume",
				},
			},
		},
		nil, nil, "",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache helper container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

		return nil, fmt.Errorf("failed to start cache helper container: %w", err)
	}

	// Build tar command: tar cf - -C /volume path1 path2 ...
	tarCmd := append([]string{"tar", "cf", "-", "-C", "/volume"}, filePaths...)

	execCfg, err := d.client.ContainerExecCreate(ctx, resp.ID, container.ExecOptions{
		Cmd:          tarCmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		_ = d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

		return nil, fmt.Errorf("failed to create exec: %w", err)
	}

	attach, err := d.client.ContainerExecAttach(ctx, execCfg.ID, container.ExecAttachOptions{})
	if err != nil {
		_ = d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

		return nil, fmt.Errorf("failed to attach exec: %w", err)
	}

	// Docker multiplexes stdout/stderr over one connection. Use stdcopy to
	// demux. Pipe the stdout portion back to the caller.
	pr, pw := io.Pipe()

	go func() {
		_, err := stdcopy.StdCopy(pw, io.Discard, attach.Reader)
		if err != nil {
			pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}

		attach.Close()
		_ = d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	}()

	return pr, nil
}

var _ cache.VolumeDataAccessor = (*Docker)(nil)
