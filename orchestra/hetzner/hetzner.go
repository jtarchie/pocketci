package hetzner

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"golang.org/x/crypto/ssh"
)

// Default values.
const (
	DefaultImage         = "docker-ce" // Hetzner app image with Docker pre-installed
	DefaultServerType    = "cx23"      // 2 vCPU, 4GB RAM (smallest shared vCPU)
	DefaultLocation      = "nbg1"      // Nuremberg, Germany
	DefaultSSHTimeout    = 5 * time.Minute
	DefaultDockerTimeout = 5 * time.Minute
	DefaultMaxWorkers    = 1
	DefaultPollInterval  = 10 * time.Second
	DefaultWaitTimeout   = 10 * time.Minute
)

// ServerConfig holds server-level configuration for the Hetzner Cloud driver.
type ServerConfig struct {
	Token         string             `json:"token,omitempty"`          // Hetzner Cloud API token (required)
	Image         string             `json:"image,omitempty"`          // Server image name (default: docker-ce)
	ServerType    string             `json:"server_type,omitempty"`    // Server type or "auto" (default: cx23)
	Location      string             `json:"location,omitempty"`       // Server location (default: nbg1)
	MaxWorkers    int                `json:"max_workers,omitempty"`    // Max concurrent servers (default: 1)
	ReuseWorker   bool               `json:"reuse_worker,omitempty"`   // Reuse idle servers across runs
	Labels        string             `json:"labels,omitempty"`         // Comma-separated key=value labels
	DiskSize      int                `json:"disk_size,omitempty"`      // Volume disk size in GB (default: 10)
	SSHTimeout    orchestra.Duration `json:"ssh_timeout,omitempty"`    // Timeout for SSH to become available (default: 5m)
	DockerTimeout orchestra.Duration `json:"docker_timeout,omitempty"` // Timeout for Docker to become available (default: 5m)
	PollInterval  orchestra.Duration `json:"poll_interval,omitempty"`  // Poll interval for worker slot (default: 10s)
	WaitTimeout   orchestra.Duration `json:"wait_timeout,omitempty"`   // Timeout waiting for worker slot (default: 10m)
}

// DriverName implements orchestra.DriverConfig.
func (ServerConfig) DriverName() string { return "hetzner" }

// Config holds configuration for the Hetzner Cloud driver.
type Config struct {
	ServerConfig
	Namespace string // Per-execution namespace identifier
}

// sanitizeHostname converts a string to a valid hostname.
// Hetzner server names only allow: a-z, A-Z, 0-9, and -
func sanitizeHostname(name string) string {
	var result strings.Builder

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			result.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			result.WriteRune(r)
		case r >= '0' && r <= '9':
			result.WriteRune(r)
		case r == '-':
			result.WriteRune(r)
		default:
			// Replace invalid characters with hyphen
			result.WriteRune('-')
		}
	}

	return result.String()
}

// Hetzner implements orchestra.Driver by creating a Hetzner Cloud server
// that runs Docker and delegates container operations to the docker driver.
type Hetzner struct {
	client     *hcloud.Client
	logger     *slog.Logger
	namespace  string
	cfg        Config
	server     *hcloud.Server
	sshKey     *hcloud.SSHKey
	sshKeyPath string

	// SSH connection to the server for Docker communication
	sshClient *ssh.Client

	// Underlying docker driver connected to the server
	dockerDriver orchestra.Driver

	// Worker pool settings
	maxWorkers   int
	reuseWorker  bool
	pollInterval time.Duration
	waitTimeout  time.Duration
}

// New creates a new Hetzner Cloud driver instance.
func New(_ context.Context, cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	if cfg.Token == "" {
		return nil, errors.New("hetzner: token is required")
	}

	client := hcloud.NewClient(hcloud.WithToken(cfg.Token))

	// Sanitize namespace to ensure it contains only valid hostname characters
	sanitizedNamespace := sanitizeHostname(cfg.Namespace)

	maxWorkers := cfg.MaxWorkers
	if maxWorkers < 1 {
		maxWorkers = DefaultMaxWorkers
	}

	pollInterval := cfg.PollInterval.Std()
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}

	waitTimeout := cfg.WaitTimeout.Std()
	if waitTimeout <= 0 {
		waitTimeout = DefaultWaitTimeout
	}

	return &Hetzner{
		client:       client,
		logger:       logger,
		namespace:    sanitizedNamespace,
		cfg:          cfg,
		maxWorkers:   maxWorkers,
		reuseWorker:  cfg.ReuseWorker,
		pollInterval: pollInterval,
		waitTimeout:  waitTimeout,
	}, nil
}

func (h *Hetzner) Name() string {
	return "hetzner"
}

// workerLabelSelector returns the Hetzner label selector for all pool machines in this namespace.
func (h *Hetzner) workerLabelSelector() string { return "pocketci-worker=" + h.namespace }

// waitForWorkerSlot blocks until a worker slot is available (total pool < maxWorkers).
// If reuseWorker is enabled and the pool is full, it attempts to claim an idle machine.
// Returns (true, nil) if an idle machine was claimed and the driver is now connected.
// Returns (false, nil) if a slot is free and the caller should create a new machine.
func (h *Hetzner) waitForWorkerSlot(ctx context.Context) (bool, error) {
	var deadline time.Time
	if h.waitTimeout > 0 {
		deadline = time.Now().Add(h.waitTimeout)
	}

	for {
		// Count all worker servers (idle + busy) in this namespace's pool
		servers, err := h.client.Server.AllWithOpts(ctx, hcloud.ServerListOpts{
			ListOpts: hcloud.ListOpts{LabelSelector: h.workerLabelSelector()},
		})
		if err != nil {
			return false, fmt.Errorf("failed to list worker servers: %w", err)
		}

		if len(servers) < h.maxWorkers {
			// Slot available, caller should create a new machine
			return false, nil
		}

		// At cap. If reuse_worker is enabled, try to claim an idle machine.
		if h.reuseWorker {
			claimed, err := h.claimIdleServer(ctx)
			if err != nil {
				return false, fmt.Errorf("failed to claim idle server: %w", err)
			}
			if claimed {
				return true, nil
			}
		}

		// No slot available; check timeout
		if !deadline.IsZero() && time.Now().After(deadline) {
			return false, fmt.Errorf("timeout waiting for worker slot after %s", h.waitTimeout)
		}

		h.logger.Info("hetzner.worker.waiting", "current", len(servers), "max", h.maxWorkers)

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(h.pollInterval):
		}
	}
}

// claimIdleServer attempts to find and exclusively claim an idle worker server.
// It transitions the machine from idle→busy via label update and reconnects SSH+Docker.
// Returns true if a machine was successfully claimed.
func (h *Hetzner) claimIdleServer(ctx context.Context) (bool, error) {
	idleServers, err := h.client.Server.AllWithOpts(ctx, hcloud.ServerListOpts{
		ListOpts: hcloud.ListOpts{LabelSelector: h.workerLabelSelector() + ",ci-worker-status=idle"},
	})
	if err != nil {
		return false, fmt.Errorf("failed to list idle servers: %w", err)
	}

	for _, server := range idleServers {
		// Build updated labels: copy existing and set status to busy
		newLabels := make(map[string]string, len(server.Labels))
		for k, v := range server.Labels {
			newLabels[k] = v
		}
		newLabels["pocketci-worker-status"] = "busy"

		_, _, err := h.client.Server.Update(ctx, server, hcloud.ServerUpdateOpts{
			Labels: newLabels,
		})
		if err != nil {
			h.logger.Warn("hetzner.worker.claim.update.error", "server_id", server.ID, "err", err)
			continue
		}

		// Re-fetch to verify we won the race
		freshServer, _, err := h.client.Server.GetByID(ctx, server.ID)
		if err != nil {
			h.logger.Warn("hetzner.worker.claim.refetch.error", "server_id", server.ID, "err", err)
			continue
		}

		if freshServer.Labels["pocketci-worker-status"] != "busy" {
			h.logger.Debug("hetzner.worker.claim.lost_race", "server_id", server.ID)
			continue
		}

		h.logger.Info("hetzner.worker.claimed_idle", "server_id", server.ID)

		// Connect to the claimed machine
		h.server = freshServer

		publicIP := freshServer.PublicNet.IPv4.IP.String()

		if err := h.waitForSSH(ctx, publicIP); err != nil {
			return false, fmt.Errorf("failed to connect SSH to claimed server: %w", err)
		}

		if err := h.waitForDocker(ctx); err != nil {
			return false, fmt.Errorf("failed to connect Docker to claimed server: %w", err)
		}

		dockerDriver, err := docker.NewDockerWithSSH(h.namespace, h.logger, h.sshClient)
		if err != nil {
			return false, fmt.Errorf("failed to create docker driver for claimed server: %w", err)
		}

		h.dockerDriver = dockerDriver

		return true, nil
	}

	return false, nil
}

// parkServer parks the machine by transitioning it busy→idle without deleting it.
func (h *Hetzner) parkServer(ctx context.Context) error {
	if h.dockerDriver != nil {
		if err := h.dockerDriver.Close(); err != nil {
			h.logger.Warn("hetzner.docker.close_error", "err", err)
		}
	}

	if h.sshClient != nil {
		if err := h.sshClient.Close(); err != nil {
			h.logger.Warn("hetzner.ssh.close_error", "err", err)
		}
	}

	// Build updated labels with idle status
	newLabels := make(map[string]string, len(h.server.Labels))
	for k, v := range h.server.Labels {
		newLabels[k] = v
	}
	newLabels["pocketci-worker-status"] = "idle"

	_, _, err := h.client.Server.Update(ctx, h.server, hcloud.ServerUpdateOpts{
		Labels: newLabels,
	})
	if err != nil {
		h.logger.Warn("hetzner.worker.park.update.error", "err", err)
	}

	h.logger.Info("hetzner.worker.parked", "server_id", h.server.ID)

	return nil
}

// ensureServer creates a server if one doesn't exist for this driver instance.
func (h *Hetzner) ensureServer(ctx context.Context, containerLimits orchestra.ContainerLimits) error {
	if h.server != nil && h.dockerDriver != nil {
		return nil
	}

	// Ensure SSH key exists first (needed for both creating and claiming machines)
	sshKey, sshKeyPath, err := h.ensureSSHKey(ctx)
	if err != nil {
		return fmt.Errorf("failed to ensure SSH key: %w", err)
	}

	h.sshKey = sshKey
	h.sshKeyPath = sshKeyPath

	// Wait for a worker slot, potentially claiming an idle machine
	claimed, err := h.waitForWorkerSlot(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire worker slot: %w", err)
	}

	if claimed {
		return nil
	}

	h.logger.Info("hetzner.server.creating")

	image := h.cfg.Image
	if image == "" {
		image = DefaultImage
	}

	location := h.cfg.Location
	if location == "" {
		location = DefaultLocation
	}

	serverType := h.determineServerType(containerLimits)

	serverName := "pocketci-" + h.namespace

	imageResult, serverTypeResult, locationResult, err := h.lookupResources(ctx, image, serverType, location)
	if err != nil {
		return err
	}

	labels := h.buildServerLabels()

	createOpts := hcloud.ServerCreateOpts{
		Name:       serverName,
		ServerType: serverTypeResult,
		Image:      imageResult,
		Location:   locationResult,
		SSHKeys:    []*hcloud.SSHKey{sshKey},
		Labels:     labels,
	}

	h.logger.Debug("hetzner.server.create_request",
		"name", serverName,
		"location", location,
		"server_type", serverType,
		"image", image,
	)

	result, _, err := h.client.Server.Create(ctx, createOpts)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Store server immediately so Close() can clean it up even if subsequent steps fail
	h.server = result.Server

	h.logger.Info("hetzner.server.created", "id", result.Server.ID, "name", serverName)

	// Wait for server to become running
	server, err := h.waitForServer(ctx, result.Server.ID)
	if err != nil {
		return fmt.Errorf("failed to wait for server: %w", err)
	}

	h.server = server

	// Get server's public IP
	publicIP := server.PublicNet.IPv4.IP.String()

	h.logger.Info("hetzner.server.ready", "ip", publicIP)

	// Wait for SSH to be available (also stores h.sshClient)
	if err := h.waitForSSH(ctx, publicIP); err != nil {
		return fmt.Errorf("failed to wait for SSH: %w", err)
	}

	// Wait for Docker to be ready (uses the SSH client established in waitForSSH)
	if err := h.waitForDocker(ctx); err != nil {
		return fmt.Errorf("failed to wait for Docker: %w", err)
	}

	// Create docker driver connected to the server via Go's SSH library
	h.logger.Info("hetzner.docker.connecting", "ip", publicIP)

	dockerDriver, err := docker.NewDockerWithSSH(h.namespace, h.logger, h.sshClient)
	if err != nil {
		return fmt.Errorf("failed to create docker driver: %w", err)
	}

	h.dockerDriver = dockerDriver
	h.logger.Info("hetzner.docker.connected")

	return nil
}

// lookupResources looks up the Hetzner image, server type, and location by name.
func (h *Hetzner) lookupResources(
	ctx context.Context,
	image, serverType, location string,
) (*hcloud.Image, *hcloud.ServerType, *hcloud.Location, error) {
	imageResult, _, err := h.client.Image.GetByNameAndArchitecture(ctx, image, hcloud.ArchitectureX86)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get image %s: %w", image, err)
	}

	if imageResult == nil {
		return nil, nil, nil, fmt.Errorf("image %s not found", image)
	}

	serverTypeResult, _, err := h.client.ServerType.GetByName(ctx, serverType)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get server type %s: %w", serverType, err)
	}

	if serverTypeResult == nil {
		return nil, nil, nil, fmt.Errorf("server type %s not found", serverType)
	}

	locationResult, _, err := h.client.Location.GetByName(ctx, location)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get location %s: %w", location, err)
	}

	if locationResult == nil {
		return nil, nil, nil, fmt.Errorf("location %s not found", location)
	}

	return imageResult, serverTypeResult, locationResult, nil
}

// buildServerLabels constructs the labels map for a new server.
func (h *Hetzner) buildServerLabels() map[string]string {
	labels := map[string]string{
		"pocketci":               "true",
		"namespace":              h.namespace,
		"pocketci-worker":        h.namespace,
		"pocketci-worker-status": "busy",
	}

	if h.cfg.Labels != "" {
		for label := range strings.SplitSeq(h.cfg.Labels, ",") {
			label = strings.TrimSpace(label)
			if parts := strings.SplitN(label, "=", 2); len(parts) == 2 {
				key := sanitizeHostname(strings.TrimSpace(parts[0]))
				value := sanitizeHostname(strings.TrimSpace(parts[1]))
				if key != "" {
					labels[key] = value
				}
			}
		}
	}

	return labels
}

// determineServerType selects an appropriate server type based on container limits.
func (h *Hetzner) determineServerType(limits orchestra.ContainerLimits) string {
	serverType := h.cfg.ServerType
	if serverType == "" {
		serverType = DefaultServerType
	}

	if serverType != "auto" {
		return serverType
	}

	// Auto-determine size based on container limits
	// Hetzner shared vCPU server types (CX line):
	// cx23:  2 vCPU, 4GB RAM
	// cx33:  4 vCPU, 8GB RAM
	// cx43:  8 vCPU, 16GB RAM
	// cx53: 16 vCPU, 32GB RAM
	//
	// Hetzner dedicated vCPU server types (CCX line):
	// ccx13:  2 vCPU, 8GB RAM
	// ccx23:  4 vCPU, 16GB RAM
	// ccx33:  8 vCPU, 32GB RAM
	// ccx43: 16 vCPU, 64GB RAM
	// ccx53: 32 vCPU, 128GB RAM
	// ccx63: 48 vCPU, 192GB RAM

	memoryMB := limits.Memory / (1024 * 1024) // Convert bytes to MB
	cpuShares := limits.CPU

	h.logger.Debug("hetzner.size.auto",
		"memory_mb", memoryMB,
		"cpu_shares", cpuShares,
	)

	// Map container limits to server types (using shared vCPU for cost efficiency)
	// CPU shares in Docker: 1024 shares = 1 CPU core (roughly)
	switch {
	case memoryMB > 16384 || cpuShares > 8192:
		return "cx53" // 16 vCPU, 32GB RAM
	case memoryMB > 8192 || cpuShares > 4096:
		return "cx43" // 8 vCPU, 16GB RAM
	case memoryMB > 4096 || cpuShares > 2048:
		return "cx33" // 4 vCPU, 8GB RAM
	default:
		return DefaultServerType // cx23: 2 vCPU, 4GB RAM
	}
}

// ensureSSHKey creates or retrieves an SSH key for server access.
func (h *Hetzner) ensureSSHKey(ctx context.Context) (*hcloud.SSHKey, string, error) {
	keyName := "pocketci-" + h.namespace

	// Check if SSH key already exists in Hetzner
	existingKey, _, err := h.client.SSHKey.GetByName(ctx, keyName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to check for existing SSH key: %w", err)
	}

	if existingKey != nil {
		h.logger.Debug("hetzner.ssh_key.exists", "name", keyName, "id", existingKey.ID)

		// Try to find the local key file
			sshKeyPath := filepath.Join(os.TempDir(), "pocketci-hetzner-"+h.namespace)
		if _, err := os.Stat(sshKeyPath); err == nil {
			return existingKey, sshKeyPath, nil
		}

		// Key exists in Hetzner but not locally, delete and recreate
		_, err = h.client.SSHKey.Delete(ctx, existingKey)
		if err != nil {
			h.logger.Warn("hetzner.ssh_key.delete_failed", "err", err)
		}
	}

	// Generate new SSH key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate SSH key: %w", err)
	}

	// Save private key to temp file
	sshKeyPath := filepath.Join(os.TempDir(), "pocketci-hetzner-"+h.namespace)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	if err := os.WriteFile(sshKeyPath, privateKeyPEM, 0o600); err != nil {
		return nil, "", fmt.Errorf("failed to write SSH private key: %w", err)
	}

	// Generate public key
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate SSH public key: %w", err)
	}

	publicKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))

	// Create key in Hetzner
	createOpts := hcloud.SSHKeyCreateOpts{
		Name:      keyName,
		PublicKey: publicKeyStr,
		Labels: map[string]string{
			"pocketci": "true",
		},
	}

	key, _, err := h.client.SSHKey.Create(ctx, createOpts)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create SSH key in Hetzner: %w", err)
	}

	h.logger.Info("hetzner.ssh_key.created", "name", keyName, "id", key.ID)

	return key, sshKeyPath, nil
}

// waitForServer polls until the server is running.
func (h *Hetzner) waitForServer(ctx context.Context, serverID int64) (*hcloud.Server, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
				return nil, errors.New("timeout waiting for server to become running")
		case <-ticker.C:
			server, _, err := h.client.Server.GetByID(ctx, serverID)
			if err != nil {
				h.logger.Warn("hetzner.server.poll_error", "err", err)

				continue
			}

			h.logger.Debug("hetzner.server.status", "status", server.Status)

			if server.Status == hcloud.ServerStatusRunning {
				return server, nil
			}
		}
	}
}

// waitForSSH polls until SSH is accessible on the server.
func (h *Hetzner) waitForSSH(ctx context.Context, ip string) error {
	h.logger.Info("hetzner.ssh.waiting", "ip", ip)

	sshTimeout := h.cfg.SSHTimeout.Std()
	if sshTimeout <= 0 {
		sshTimeout = DefaultSSHTimeout
	}

	// Load private key
	privateKeyData, err := os.ReadFile(h.sshKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read SSH private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privateKeyData)
	if err != nil {
		return fmt.Errorf("failed to parse SSH private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // CI servers are ephemeral
		Timeout:         10 * time.Second,
	}

	deadline := time.Now().Add(sshTimeout)

	// Try immediately first, then poll
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for SSH after %s", sshTimeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := ssh.Dial("tcp", ip+":22", config)
		if err != nil {
			h.logger.Debug("hetzner.ssh.connecting", "ip", ip, "err", err)
			time.Sleep(5 * time.Second)

			continue
		}

		// Store the connection for reuse by waitForDocker
		h.sshClient = conn
		h.logger.Info("hetzner.ssh.connected")

		return nil
	}
}

// waitForDocker polls until Docker is accessible on the server.
// Uses the existing SSH client connection established in waitForSSH.
func (h *Hetzner) waitForDocker(ctx context.Context) error {
	h.logger.Info("hetzner.docker.waiting")

	dockerTimeout := h.cfg.DockerTimeout.Std()
	if dockerTimeout <= 0 {
		dockerTimeout = DefaultDockerTimeout
	}

	if h.sshClient == nil {
		return errors.New("SSH client not connected")
	}

	deadline := time.Now().Add(dockerTimeout)

	// Try immediately first, then poll
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for Docker after %s", dockerTimeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		session, err := h.sshClient.NewSession()
		if err != nil {
			h.logger.Debug("hetzner.docker.session_error", "err", err)
			time.Sleep(5 * time.Second)

			continue
		}

		output, err := session.CombinedOutput("docker ps")
		_ = session.Close()

		if err != nil {
			h.logger.Debug("hetzner.docker.check_error", "err", err, "output", string(output))
			time.Sleep(5 * time.Second)

			continue
		}

		h.logger.Info("hetzner.docker.ready")

		return nil
	}
}

// RunContainer creates the server if needed and delegates to the docker driver.
func (h *Hetzner) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	if err := h.ensureServer(ctx, task.ContainerLimits); err != nil {
		return nil, fmt.Errorf("failed to ensure server: %w", err)
	}

	return h.dockerDriver.RunContainer(ctx, task)
}

// GetContainer attempts to find and return an existing container by its ID.
// Delegates to the docker driver after ensuring the server exists.
func (h *Hetzner) GetContainer(ctx context.Context, containerID string) (orchestra.Container, error) {
	if h.dockerDriver == nil {
		return nil, orchestra.ErrContainerNotFound
	}
	return h.dockerDriver.GetContainer(ctx, containerID)
}

// CreateVolume creates a volume on the server's docker instance.
func (h *Hetzner) CreateVolume(ctx context.Context, name string, size int) (orchestra.Volume, error) {
	if err := h.ensureServer(ctx, orchestra.ContainerLimits{}); err != nil {
		return nil, fmt.Errorf("failed to ensure server: %w", err)
	}

	// Get disk size from config if not specified
	if size <= 0 {
		size = h.cfg.DiskSize
		if size <= 0 {
			size = 10
		}
	}

	// For now, delegate to docker driver's volume creation
	// In the future, we could create Hetzner block storage volumes for larger needs
	return h.dockerDriver.CreateVolume(ctx, name, size)
}

// Close either parks the machine (if reuse_worker=true) or deletes it and cleans up resources.
func (h *Hetzner) Close() error {
	ctx := context.Background()

	// If reuse_worker is enabled, park the machine instead of deleting it
	if h.reuseWorker && h.server != nil {
		return h.parkServer(ctx)
	}

	// Close docker driver first
	if h.dockerDriver != nil {
		if err := h.dockerDriver.Close(); err != nil {
			h.logger.Warn("hetzner.docker.close_error", "err", err)
		}
	}

	// Close SSH client
	if h.sshClient != nil {
		if err := h.sshClient.Close(); err != nil {
			h.logger.Warn("hetzner.ssh.close_error", "err", err)
		}
	}

	// Delete server
	if h.server != nil {
		h.logger.Info("hetzner.server.deleting", "id", h.server.ID)

		_, _, err := h.client.Server.DeleteWithResult(ctx, h.server)
		if err != nil {
			h.logger.Error("hetzner.server.delete_error", "err", err)

			return fmt.Errorf("failed to delete server: %w", err)
		}

		h.logger.Info("hetzner.server.deleted", "id", h.server.ID)
	}

	// Delete SSH key from Hetzner
	if h.sshKey != nil {
		_, err := h.client.SSHKey.Delete(ctx, h.sshKey)
		if err != nil {
			h.logger.Warn("hetzner.ssh_key.delete_error", "err", err)
		}
	}

	// Delete local SSH key file
	if h.sshKeyPath != "" {
		if err := os.Remove(h.sshKeyPath); err != nil && !os.IsNotExist(err) {
			h.logger.Warn("hetzner.ssh_key.local.delete.error", "err", err)
		}
	}

	return nil
}

// CleanupOrphanedResources deletes servers and SSH keys matching the specified label selector.
// If labelSelector is empty, it defaults to "pocketci=true" which matches all PocketCI-created resources.
// For more targeted cleanup, use a specific selector like "environment=test" or "namespace=myns".
// This is useful for cleaning up resources from failed or interrupted runs.
func CleanupOrphanedResources(ctx context.Context, token string, logger *slog.Logger, labelSelector string) error {
	if labelSelector == "" {
		labelSelector = "pocketci=true"
	}

	client := hcloud.NewClient(hcloud.WithToken(token))

	// List all servers with the specified label selector
	servers, err := client.Server.AllWithOpts(ctx, hcloud.ServerListOpts{
		ListOpts: hcloud.ListOpts{
			LabelSelector: labelSelector,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to list servers: %w", err)
	}

	for _, server := range servers {
		logger.Info("hetzner.cleanup.server.deleting", "id", server.ID, "name", server.Name, "selector", labelSelector)

		_, _, err := client.Server.DeleteWithResult(ctx, server)
		if err != nil {
			logger.Warn("hetzner.cleanup.server.delete.error", "id", server.ID, "err", err)
		} else {
			logger.Info("hetzner.cleanup.server.delete.success", "id", server.ID)
		}
	}

	// List all SSH keys and delete those matching the pattern
	// SSH keys are named "pocketci-<namespace>" so we derive prefix from the label selector
	keyPrefix := "pocketci-"

	// If selector includes namespace, use it for more targeted cleanup
	if strings.Contains(labelSelector, "namespace=") {
		for part := range strings.SplitSeq(labelSelector, ",") {
			part = strings.TrimSpace(part)
			if after, ok := strings.CutPrefix(part, "namespace="); ok {
				ns := after
				keyPrefix = "pocketci-" + ns

				break
			}
		}
	}

	keys, err := client.SSHKey.All(ctx)
	if err != nil {
		return fmt.Errorf("failed to list SSH keys: %w", err)
	}

	for _, key := range keys {
		if strings.HasPrefix(key.Name, keyPrefix) {
			logger.Info("hetzner.cleanup.deleting_ssh_key.start", "id", key.ID, "name", key.Name)

			_, err := client.SSHKey.Delete(ctx, key)
			if err != nil {
				logger.Warn("hetzner.cleanup.ssh_key.delete.error", "id", key.ID, "err", err)
			} else {
				logger.Info("hetzner.cleanup.ssh_key_deleted", "id", key.ID)
			}
		}
	}

	return nil
}

var _ orchestra.Driver = &Hetzner{}
