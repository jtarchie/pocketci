package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/cli/cli/connhelper"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jtarchie/pocketci/orchestra"
	"golang.org/x/crypto/ssh"
)

// ServerConfig holds server-level configuration for the Docker driver.
type ServerConfig struct {
	Host string `json:"host,omitempty"` // Docker daemon host URL (e.g., "tcp://host:2376", "ssh://user@host"); defaults to DOCKER_HOST env var
}

// DriverName implements orchestra.DriverConfig.
func (ServerConfig) DriverName() string { return "docker" }

// Config holds the full configuration for the Docker driver.
type Config struct {
	ServerConfig
	Namespace string // Per-execution namespace identifier
}

type Docker struct {
	client    *client.Client
	logger    *slog.Logger
	namespace string
}

// Close implements orchestra.Driver.
func (d *Docker) Close() error {
	// find all containers in the namespace and remove them
	attempts := 5
	for currentAttempt := range attempts {
		_, err := d.client.ContainersPrune(context.Background(), filters.NewArgs(
			filters.Arg("label", "orchestra.namespace="+d.namespace),
		))
		if err == nil {
			break
		}

		if !errdefs.IsConflict(err) {
			return fmt.Errorf("failed to prune containers: %w", err)
		}

		if currentAttempt < attempts-1 {
			time.Sleep(time.Duration(1<<currentAttempt) * time.Second) // exponential backoff
		} else {
			return fmt.Errorf("failed to prune containers after %d attempts: %w", attempts, err)
		}
	}

	// find all volumes in the namespace and remove them
	volumes, err := d.client.VolumeList(context.Background(), volume.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", "orchestra.namespace="+d.namespace),
		),
	})
	if err != nil {
		return fmt.Errorf("failed to list volumes: %w", err)
	}

	for _, volume := range volumes.Volumes {
		err := d.client.VolumeRemove(context.Background(), volume.Name, true)
		if err != nil {
			return fmt.Errorf("failed to remove volume %s: %w", volume.Name, err)
		}
	}

	return nil
}

func New(cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	var clientOpts []client.Opt

	dockerHost := cfg.Host

	if strings.HasPrefix(dockerHost, "ssh://") {
		// https://gist.github.com/agbaraka/654a218f8ea13b3da8a47d47595f5d05
		helper, err := connhelper.GetConnectionHelper(dockerHost)
		if err != nil {
			return nil, fmt.Errorf("failed to get connection helper: %w", err)
		}

		httpClient := &http.Client{
			Transport: &http.Transport{
				DialContext: helper.Dialer,
			},
		}

		clientOpts = append(clientOpts,
			client.WithHTTPClient(httpClient),
			client.WithHost(helper.Host),
			client.WithDialContext(helper.Dialer),
			client.WithAPIVersionNegotiation(),
		)
	} else {
		clientOpts = append(clientOpts, client.FromEnv, client.WithAPIVersionNegotiation())
	}

	cli, err := client.NewClientWithOpts(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Docker{
		client:    cli,
		logger:    logger,
		namespace: cfg.Namespace,
	}, nil
}

// NewDockerWithSSH creates a Docker driver that communicates over an existing SSH connection.
// This uses Go's native SSH library to tunnel to the Docker socket, avoiding the need
// for the host's ssh command or ssh-agent.
func NewDockerWithSSH(namespace string, logger *slog.Logger, sshClient *ssh.Client) (orchestra.Driver, error) {
	// Create a custom dialer that tunnels through SSH to the Docker Unix socket
	sshDialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Always dial the Docker Unix socket through SSH, ignoring the network/addr
		return sshClient.Dial("unix", "/var/run/docker.sock")
	}

	// Create a custom HTTP transport that uses our SSH dialer
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: sshDialer,
		},
	}

	cli, err := client.NewClientWithOpts(
		client.WithHTTPClient(httpClient),
		client.WithHost("http://localhost"), // Dummy host, actual connection is via Unix socket over SSH
		client.WithDialContext(sshDialer),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Docker{
		client:    cli,
		logger:    logger,
		namespace: namespace,
	}, nil
}

func (d *Docker) Name() string {
	return "docker"
}

// GetContainer finds and returns an existing container by its ID.
// Returns ErrContainerNotFound if the container does not exist.
func (d *Docker) GetContainer(ctx context.Context, containerID string) (orchestra.Container, error) {
	_, err := d.client.ContainerInspect(ctx, containerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, orchestra.ErrContainerNotFound
		}
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	return &Container{
		id:     containerID,
		client: d.client,
	}, nil
}

var (
	_ orchestra.Driver          = &Docker{}
	_ orchestra.Container       = &Container{}
	_ orchestra.ContainerStatus = &ContainerStatus{}
	_ orchestra.Volume          = &Volume{}
)
