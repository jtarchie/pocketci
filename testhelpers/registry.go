package testhelpers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/onsi/gomega"
)

// Registry is a handle to a registry:2 container started for testing.
// Use Endpoint to push/pull from inside another container on the same Docker
// host (the endpoint resolves to the registry container's bridge-network IP).
type Registry struct {
	id       string
	hostPort int
	bridgeIP string
	client   *client.Client
}

// StartRegistry boots a registry:2 container in the background and returns a
// handle. Containers running on the same Docker host (the default bridge
// network) can reach this registry at Endpoint(); the host can also reach it
// at HostEndpoint(). The container is removed via t.Cleanup.
//
// Tests should skip when Docker is unavailable:
//
//	if _, err := exec.LookPath("docker"); err != nil { t.Skip("docker required") }
func StartRegistry(t *testing.T) *Registry {
	t.Helper()

	g := gomega.NewWithT(t)

	_, dockerErr := exec.LookPath("docker")
	if dockerErr != nil {
		t.Skip("docker not installed; skipping integration test")
	}

	ctx := context.Background()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	g.Expect(err).NotTo(gomega.HaveOccurred(), "docker client")

	pullReader, err := cli.ImagePull(ctx, "registry:2", image.PullOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "pull registry:2")

	_, _ = io.Copy(io.Discard, pullReader)
	_ = pullReader.Close()

	containerName := "pocketci-test-registry-" + strings.ToLower(gonanoid.MustGenerate("abcdefghijklmnopqrstuvwxyz0123456789", 12))

	resp, err := cli.ContainerCreate(
		ctx,
		&container.Config{
			Image:        "registry:2",
			ExposedPorts: nat.PortSet{"5000/tcp": {}},
			Env: []string{
				"REGISTRY_STORAGE_DELETE_ENABLED=true",
			},
		},
		&container.HostConfig{
			AutoRemove:      false, // we explicitly remove in cleanup
			PublishAllPorts: true,
		},
		&network.NetworkingConfig{},
		nil,
		containerName,
	)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "create registry container")

	err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred(), "start registry container")

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = cli.ContainerRemove(stopCtx, resp.ID, container.RemoveOptions{Force: true})
		_ = cli.Close()
	})

	inspect, err := cli.ContainerInspect(ctx, resp.ID)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "inspect registry container")

	bridgeIP := ""
	if bridgeNet, ok := inspect.NetworkSettings.Networks["bridge"]; ok && bridgeNet != nil {
		bridgeIP = bridgeNet.IPAddress
	}

	g.Expect(bridgeIP).NotTo(gomega.BeEmpty(), "registry container should have a bridge IP")

	hostPort := 0

	if bindings, ok := inspect.NetworkSettings.Ports["5000/tcp"]; ok && len(bindings) > 0 {
		port, err := strconv.Atoi(bindings[0].HostPort)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "parse host port")

		hostPort = port
	}

	reg := &Registry{
		id:       resp.ID,
		hostPort: hostPort,
		bridgeIP: bridgeIP,
		client:   cli,
	}

	// Wait for the registry to accept connections on the host-published port.
	g.Eventually(func() bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reg.HostEndpoint()+"/v2/", nil)
		if err != nil {
			return false
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}

		_ = resp.Body.Close()

		return resp.StatusCode == http.StatusOK
	}, "30s", "200ms").Should(gomega.BeTrue(), "registry should accept connections")

	return reg
}

// Endpoint returns "<bridge-ip>:5000" — the address other containers on the
// default Docker bridge network can use to talk to this registry.
func (r *Registry) Endpoint() string {
	return r.bridgeIP + ":5000"
}

// HostEndpoint returns "http://localhost:<published-port>" — useful for tests
// running on the Docker host that need to inspect the registry over HTTP.
func (r *Registry) HostEndpoint() string {
	if r.hostPort == 0 {
		return ""
	}

	return fmt.Sprintf("http://localhost:%d", r.hostPort)
}

// ID returns the Docker container ID of the registry.
func (r *Registry) ID() string {
	return r.id
}
