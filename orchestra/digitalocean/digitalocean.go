package digitalocean

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
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"golang.org/x/crypto/ssh"
)

// Default values.
const (
	DefaultImage         = "docker-20-04"
	DefaultSize          = "s-1vcpu-1gb"
	DefaultRegion        = "nyc3"
	DefaultDiskSizeGB    = 25              // Default disk size in GB
	DefaultSSHTimeout    = 5 * time.Minute // Default timeout for SSH to become available
	DefaultDockerTimeout = 5 * time.Minute // Default timeout for Docker to become available
	DefaultMaxWorkers    = 1
	DefaultPollInterval  = 10 * time.Second
	DefaultWaitTimeout   = 10 * time.Minute
)

// ServerConfig holds server-level configuration for the DigitalOcean driver.
type ServerConfig struct {
	Token         string             `json:"token,omitempty"`          // DigitalOcean API token (required)
	Image         string             `json:"image,omitempty"`          // Droplet image slug (default: docker-20-04)
	Size          string             `json:"size,omitempty"`           // Droplet size slug or "auto" (default: s-1vcpu-1gb)
	Region        string             `json:"region,omitempty"`         // Droplet region (default: nyc3)
	DiskSize      int                `json:"disk_size,omitempty"`      // Volume disk size in GB (default: 25)
	MaxWorkers    int                `json:"max_workers,omitempty"`    // Max concurrent droplets (default: 1)
	ReuseWorker   bool               `json:"reuse_worker,omitempty"`   // Reuse idle droplets across runs
	Tags          string             `json:"tags,omitempty"`           // Comma-separated custom tags
	SSHTimeout    orchestra.Duration `json:"ssh_timeout,omitempty"`    // Timeout for SSH to become available (default: 5m)
	DockerTimeout orchestra.Duration `json:"docker_timeout,omitempty"` // Timeout for Docker to become available (default: 5m)
	PollInterval  orchestra.Duration `json:"poll_interval,omitempty"`  // Poll interval for worker slot (default: 10s)
	WaitTimeout   orchestra.Duration `json:"wait_timeout,omitempty"`   // Timeout waiting for worker slot (default: 10m)
}

// DriverName implements orchestra.DriverConfig.
func (ServerConfig) DriverName() string { return "digitalocean" }

// Config holds configuration for the DigitalOcean driver.
type Config struct {
	ServerConfig
	Namespace string // Per-execution namespace identifier
}

// sanitizeHostname converts a string to a valid hostname.
// DigitalOcean hostnames only allow: a-z, A-Z, 0-9, . and -
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
		case r == '.' || r == '-':
			result.WriteRune(r)
		default:
			// Replace invalid characters with hyphen
			result.WriteRune('-')
		}
	}
	return result.String()
}

// DigitalOcean implements orchestra.Driver by creating a DigitalOcean droplet
// that runs Docker and delegates container operations to the docker driver.
type DigitalOcean struct {
	client     *godo.Client
	logger     *slog.Logger
	namespace  string
	cfg        Config
	droplet    *godo.Droplet
	sshKeyID   int
	sshKeyPath string

	// SSH connection to the droplet for Docker communication
	sshClient *ssh.Client

	// Underlying docker driver connected to the droplet
	dockerDriver orchestra.Driver

	// Worker pool settings
	maxWorkers   int
	reuseWorker  bool
	pollInterval time.Duration
	waitTimeout  time.Duration
}

// New creates a new DigitalOcean driver instance.
func New(_ context.Context, cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	if cfg.Token == "" {
		return nil, errors.New("digitalocean: token is required")
	}

	client := godo.NewFromToken(cfg.Token)

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

	return &DigitalOcean{
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

func (d *DigitalOcean) Name() string {
	return "digitalocean"
}

// workerTag returns the tag used to identify all worker machines in the pool for this namespace.
func (d *DigitalOcean) workerTag() string { return "pocketci-worker-" + d.namespace }

// busyTag returns the tag applied to machines currently claimed by a pipeline run.
func (d *DigitalOcean) busyTag() string { return "pocketci-busy-" + d.namespace }

// idleTag returns the tag applied to parked machines available for reuse.
func (d *DigitalOcean) idleTag() string { return "pocketci-idle-" + d.namespace }

// waitForWorkerSlot blocks until a worker slot is available (total pool < maxWorkers).
// If reuseWorker is enabled and the pool is full, it attempts to claim an idle machine.
// Returns (true, nil) if an idle machine was claimed and the driver is now connected.
// Returns (false, nil) if a slot is free and the caller should create a new machine.
func (d *DigitalOcean) waitForWorkerSlot(ctx context.Context) (bool, error) {
	var deadline time.Time
	if d.waitTimeout > 0 {
		deadline = time.Now().Add(d.waitTimeout)
	}

	for {
		// Count all worker droplets (idle + busy) in this namespace's pool
		droplets, _, err := d.client.Droplets.ListByTag(ctx, d.workerTag(), &godo.ListOptions{PerPage: 200})
		if err != nil {
			return false, fmt.Errorf("failed to list worker droplets: %w", err)
		}

		if len(droplets) < d.maxWorkers {
			// Slot available, caller should create a new machine
			return false, nil
		}

		// At cap. If reuse_worker is enabled, try to claim an idle machine.
		if d.reuseWorker {
			claimed, err := d.claimIdleDroplet(ctx)
			if err != nil {
				return false, fmt.Errorf("failed to claim idle droplet: %w", err)
			}
			if claimed {
				return true, nil
			}
		}

		// No slot available; check timeout
		if !deadline.IsZero() && time.Now().After(deadline) {
			return false, fmt.Errorf("timeout waiting for worker slot after %s", d.waitTimeout)
		}

		d.logger.Info("digitalocean.worker.waiting", "current", len(droplets), "max", d.maxWorkers)

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(d.pollInterval):
		}
	}
}

// claimIdleDroplet attempts to find and exclusively claim an idle worker droplet.
// It transitions the machine from idle→busy via tag manipulation and reconnects SSH+Docker.
// Returns true if a machine was successfully claimed.
func (d *DigitalOcean) claimIdleDroplet(ctx context.Context) (bool, error) {
	idleDroplets, _, err := d.client.Droplets.ListByTag(ctx, d.idleTag(), &godo.ListOptions{PerPage: 200})
	if err != nil {
		return false, fmt.Errorf("failed to list idle droplets: %w", err)
	}

	for i := range idleDroplets {
		droplet := &idleDroplets[i]
		dropletID := strconv.Itoa(droplet.ID)

		// Remove idle tag first, then add busy tag (best-effort optimistic claim)
		_, err = d.client.Tags.UntagResources(ctx, d.idleTag(), &godo.UntagResourcesRequest{
			Resources: []godo.Resource{{ID: dropletID, Type: godo.DropletResourceType}},
		})
		if err != nil {
			d.logger.Warn("digitalocean.worker.claim.untag_idle.error", "droplet_id", droplet.ID, "err", err)
			continue
		}

		_, err = d.client.Tags.TagResources(ctx, d.busyTag(), &godo.TagResourcesRequest{
			Resources: []godo.Resource{{ID: dropletID, Type: godo.DropletResourceType}},
		})
		if err != nil {
			d.logger.Warn("digitalocean.worker.claim.tag_busy.error", "droplet_id", droplet.ID, "err", err)
			continue
		}

		// Re-fetch to verify we won the race (another concurrent instance may have claimed it)
		freshDroplet, _, err := d.client.Droplets.Get(ctx, droplet.ID)
		if err != nil {
			d.logger.Warn("digitalocean.worker.claim.refetch.error", "droplet_id", droplet.ID, "err", err)
			continue
		}

		hasBusy, hasIdle := false, false
		for _, tag := range freshDroplet.Tags {
			if tag == d.busyTag() {
				hasBusy = true
			}
			if tag == d.idleTag() {
				hasIdle = true
			}
		}

		if !hasBusy || hasIdle {
			d.logger.Debug("digitalocean.worker.claim.lost_race", "droplet_id", droplet.ID)
			continue
		}

		d.logger.Info("digitalocean.worker.claimed_idle", "droplet_id", droplet.ID)

		// Connect to the claimed machine
		d.droplet = freshDroplet

		publicIP, err := freshDroplet.PublicIPv4()
		if err != nil {
			return false, fmt.Errorf("failed to get claimed droplet IP: %w", err)
		}

		if err := d.waitForSSH(ctx, publicIP); err != nil {
			return false, fmt.Errorf("failed to connect SSH to claimed droplet: %w", err)
		}

		if err := d.waitForDocker(ctx); err != nil {
			return false, fmt.Errorf("failed to connect Docker to claimed droplet: %w", err)
		}

		dockerDriver, err := docker.NewDockerWithSSH(d.namespace, d.logger, d.sshClient)
		if err != nil {
			return false, fmt.Errorf("failed to create docker driver for claimed droplet: %w", err)
		}

		d.dockerDriver = dockerDriver

		return true, nil
	}

	return false, nil
}

// parkDroplet parks the machine by transitioning it busy→idle without deleting it.
func (d *DigitalOcean) parkDroplet(ctx context.Context) error {
	if d.dockerDriver != nil {
		if err := d.dockerDriver.Close(); err != nil {
			d.logger.Warn("digitalocean.docker.close.error", "err", err)
		}
	}

	if d.sshClient != nil {
		if err := d.sshClient.Close(); err != nil {
			d.logger.Warn("digitalocean.ssh.close.error", "err", err)
		}
	}

	dropletID := strconv.Itoa(d.droplet.ID)

	// Remove busy tag
	_, err := d.client.Tags.UntagResources(ctx, d.busyTag(), &godo.UntagResourcesRequest{
		Resources: []godo.Resource{{ID: dropletID, Type: godo.DropletResourceType}},
	})
	if err != nil {
		d.logger.Warn("digitalocean.worker.park.untag_busy.error", "err", err)
	}

	// Add idle tag
	_, err = d.client.Tags.TagResources(ctx, d.idleTag(), &godo.TagResourcesRequest{
		Resources: []godo.Resource{{ID: dropletID, Type: godo.DropletResourceType}},
	})
	if err != nil {
		d.logger.Warn("digitalocean.worker.park.tag_idle.error", "err", err)
	}

	d.logger.Info("digitalocean.worker.parked", "droplet_id", d.droplet.ID)

	return nil
}

// ensureDroplet creates a droplet if one doesn't exist for this driver instance.
func (d *DigitalOcean) ensureDroplet(ctx context.Context, containerLimits orchestra.ContainerLimits) error {
	if d.droplet != nil && d.dockerDriver != nil {
		return nil
	}

	// Ensure SSH key exists first (needed for both creating and claiming machines)
	sshKeyID, sshKeyPath, err := d.ensureSSHKey(ctx)
	if err != nil {
		return fmt.Errorf("failed to ensure SSH key: %w", err)
	}

	d.sshKeyID = sshKeyID
	d.sshKeyPath = sshKeyPath

	// Wait for a worker slot, potentially claiming an idle machine
	claimed, err := d.waitForWorkerSlot(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire worker slot: %w", err)
	}

	if claimed {
		return nil
	}

	d.logger.Info("digitalocean.droplet.create")

	createRequest := d.buildDropletCreateRequest(containerLimits, sshKeyID)

	droplet, _, err := d.client.Droplets.Create(ctx, createRequest)
	if err != nil {
		return fmt.Errorf("failed to create droplet: %w", err)
	}

	// Store droplet immediately so Close() can clean it up even if subsequent steps fail
	d.droplet = droplet

	d.logger.Info("droplet.create.success", "id", droplet.ID, "name", createRequest.Name)

	// Wait for droplet to become active and get its public IP
	droplet, err = d.waitForDroplet(ctx, droplet.ID)
	if err != nil {
		return fmt.Errorf("failed to wait for droplet: %w", err)
	}

	d.droplet = droplet

	// Get droplet's public IP
	publicIP, err := droplet.PublicIPv4()
	if err != nil {
		return fmt.Errorf("failed to get droplet public IP: %w", err)
	}

	d.logger.Info("digitalocean.droplet.ready", "ip", publicIP)

	// Wait for SSH to be available (also stores d.sshClient)
	if err := d.waitForSSH(ctx, publicIP); err != nil {
		return fmt.Errorf("failed to wait for SSH: %w", err)
	}

	// Wait for Docker to be ready (uses the SSH client established in waitForSSH)
	if err := d.waitForDocker(ctx); err != nil {
		return fmt.Errorf("failed to wait for Docker: %w", err)
	}

	// Create docker driver connected to the droplet via Go's SSH library
	d.logger.Info("digitalocean.docker.connect", "ip", publicIP)

	dockerDriver, err := docker.NewDockerWithSSH(d.namespace, d.logger, d.sshClient)
	if err != nil {
		return fmt.Errorf("failed to create docker driver: %w", err)
	}

	d.dockerDriver = dockerDriver
	d.logger.Info("digitalocean.docker.connected.success")

	return nil
}

// buildDropletCreateRequest constructs the DropletCreateRequest with defaults and tags.
func (d *DigitalOcean) buildDropletCreateRequest(containerLimits orchestra.ContainerLimits, sshKeyID int) *godo.DropletCreateRequest {
	image := d.cfg.Image
	if image == "" {
		image = DefaultImage
	}

	region := d.cfg.Region
	if region == "" {
		region = DefaultRegion
	}

	size := d.determineDropletSize(containerLimits)

	dropletName := "pocketci-" + d.namespace

	tags := []string{
		"pocketci",
		"namespace-" + d.namespace,
		d.workerTag(),
		d.busyTag(),
	}

	if d.cfg.Tags != "" {
		for tag := range strings.SplitSeq(d.cfg.Tags, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tags = append(tags, sanitizeHostname(tag))
			}
		}
	}

	d.logger.Debug("digitalocean.droplet.create_request",
		"name", dropletName,
		"region", region,
		"size", size,
		"image", image,
	)

	return &godo.DropletCreateRequest{
		Name:   dropletName,
		Region: region,
		Size:   size,
		Image: godo.DropletCreateImage{
			Slug: image,
		},
		SSHKeys: []godo.DropletCreateSSHKey{
			{ID: sshKeyID},
		},
		Tags: tags,
	}
}

// determineDropletSize selects an appropriate droplet size based on container limits.
func (d *DigitalOcean) determineDropletSize(limits orchestra.ContainerLimits) string {
	size := d.cfg.Size
	if size == "" {
		size = DefaultSize
	}

	if size != "auto" {
		return size
	}

	// Auto-determine size based on container limits
	// Digital Ocean droplet sizes:
	// s-1vcpu-1gb:    1 vCPU, 1GB RAM
	// s-1vcpu-2gb:    1 vCPU, 2GB RAM
	// s-2vcpu-2gb:    2 vCPU, 2GB RAM
	// s-2vcpu-4gb:    2 vCPU, 4GB RAM
	// s-4vcpu-8gb:    4 vCPU, 8GB RAM
	// s-8vcpu-16gb:   8 vCPU, 16GB RAM

	memoryMB := limits.Memory / (1024 * 1024) // Convert bytes to MB
	cpuShares := limits.CPU

	d.logger.Debug("digitalocean.size",
		"memory_mb", memoryMB,
		"cpu_shares", cpuShares,
	)

	// Map container limits to droplet sizes
	// CPU shares in Docker: 1024 shares = 1 CPU core (roughly)
	// Memory is more straightforward

	switch {
	case memoryMB > 8192 || cpuShares > 4096:
		return "s-8vcpu-16gb"
	case memoryMB > 4096 || cpuShares > 2048:
		return "s-4vcpu-8gb"
	case memoryMB > 2048 || cpuShares > 1024:
		return "s-2vcpu-4gb"
	case memoryMB > 1024:
		return "s-2vcpu-2gb"
	case memoryMB > 512:
		return "s-1vcpu-2gb"
	default:
		return DefaultSize
	}
}

// ensureSSHKey creates or retrieves an SSH key for droplet access.
func (d *DigitalOcean) ensureSSHKey(ctx context.Context) (int, string, error) {
	keyName := "pocketci-" + d.namespace

	// Check if SSH key already exists in DO
	keys, _, err := d.client.Keys.List(ctx, &godo.ListOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("failed to list SSH keys: %w", err)
	}

	for _, key := range keys {
		if key.Name == keyName {
			d.logger.Debug("digitalocean.ssh_key.exists", "name", keyName, "id", key.ID)

			// Try to find the local key file
			sshKeyPath := filepath.Join(os.TempDir(), "pocketci-do-"+d.namespace)
			if _, err := os.Stat(sshKeyPath); err == nil {
				return key.ID, sshKeyPath, nil
			}

			// Key exists in DO but not locally, delete and recreate
			_, err = d.client.Keys.DeleteByID(ctx, key.ID)
			if err != nil {
				d.logger.Warn("digitalocean.ssh_key.delete.remote_failed", "err", err)
			}

			break
		}
	}

	// Generate new SSH key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return 0, "", fmt.Errorf("failed to generate SSH key: %w", err)
	}

	// Save private key to temp file
	sshKeyPath := filepath.Join(os.TempDir(), "pocketci-do-"+d.namespace)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	if err := os.WriteFile(sshKeyPath, privateKeyPEM, 0o600); err != nil {
		return 0, "", fmt.Errorf("failed to write SSH private key: %w", err)
	}

	// Generate public key
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return 0, "", fmt.Errorf("failed to generate SSH public key: %w", err)
	}

	publicKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))

	// Create key in Digital Ocean
	createRequest := &godo.KeyCreateRequest{
		Name:      keyName,
		PublicKey: publicKeyStr,
	}

	key, _, err := d.client.Keys.Create(ctx, createRequest)
	if err != nil {
		return 0, "", fmt.Errorf("failed to create SSH key in DO: %w", err)
	}

	d.logger.Info("digitalocean.ssh_key.create.success", "name", keyName, "id", key.ID)

	return key.ID, sshKeyPath, nil
}

// waitForDroplet polls until the droplet is active.
func (d *DigitalOcean) waitForDroplet(ctx context.Context, dropletID int) (*godo.Droplet, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return nil, errors.New("timeout waiting for droplet to become active")
		case <-ticker.C:
			droplet, _, err := d.client.Droplets.Get(ctx, dropletID)
			if err != nil {
				d.logger.Warn("digitalocean.droplet.poll.error", "err", err)

				continue
			}

			d.logger.Debug("digitalocean.droplet.poll", "status", droplet.Status)

			if droplet.Status == "active" {
				return droplet, nil
			}
		}
	}
}

// waitForSSH polls until SSH is accessible on the droplet.
func (d *DigitalOcean) waitForSSH(ctx context.Context, ip string) error {
	d.logger.Info("digitalocean.ssh.waiting", "ip", ip)

	sshTimeout := d.cfg.SSHTimeout.Std()
	if sshTimeout <= 0 {
		sshTimeout = DefaultSSHTimeout
	}

	// Load private key
	privateKeyData, err := os.ReadFile(d.sshKeyPath)
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
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // CI droplets are ephemeral
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
			d.logger.Debug("digitalocean.ssh.connect.error", "ip", ip, "err", err)
			time.Sleep(5 * time.Second)

			continue
		}

		// Store the connection for reuse by waitForDocker
		d.sshClient = conn
		d.logger.Info("digitalocean.ssh.connect.success")

		return nil
	}
}

// waitForDocker polls until Docker is accessible on the droplet.
// Uses the existing SSH client connection established in waitForSSH.
func (d *DigitalOcean) waitForDocker(ctx context.Context) error {
	d.logger.Info("digitalocean.docker.wait")

	dockerTimeout := d.cfg.DockerTimeout.Std()
	if dockerTimeout <= 0 {
		dockerTimeout = DefaultDockerTimeout
	}

	if d.sshClient == nil {
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

		session, err := d.sshClient.NewSession()
		if err != nil {
			d.logger.Debug("digitalocean.docker.session.error", "err", err)
			time.Sleep(5 * time.Second)

			continue
		}

		output, err := session.CombinedOutput("docker ps")
		_ = session.Close()

		if err != nil {
			d.logger.Debug("digitalocean.docker.check.error", "err", err, "output", string(output))
			time.Sleep(5 * time.Second)

			continue
		}

		d.logger.Info("digitalocean.docker.ready.success")

		return nil
	}
}

// RunContainer creates the droplet if needed and delegates to the docker driver.
func (d *DigitalOcean) RunContainer(ctx context.Context, task orchestra.Task) (orchestra.Container, error) {
	if err := d.ensureDroplet(ctx, task.ContainerLimits); err != nil {
		return nil, fmt.Errorf("failed to ensure droplet: %w", err)
	}

	return d.dockerDriver.RunContainer(ctx, task)
}

// GetContainer attempts to find and return an existing container by its ID.
// Delegates to the docker driver after ensuring the droplet exists.
func (d *DigitalOcean) GetContainer(ctx context.Context, containerID string) (orchestra.Container, error) {
	if d.dockerDriver == nil {
		return nil, orchestra.ErrContainerNotFound
	}
	return d.dockerDriver.GetContainer(ctx, containerID)
}

// CreateVolume creates a volume on the droplet's docker instance.
// The size parameter is used when creating Digital Ocean block storage volumes.
func (d *DigitalOcean) CreateVolume(ctx context.Context, name string, size int) (orchestra.Volume, error) {
	if err := d.ensureDroplet(ctx, orchestra.ContainerLimits{}); err != nil {
		return nil, fmt.Errorf("failed to ensure droplet: %w", err)
	}

	// Get disk size from config if not specified
	if size <= 0 {
		size = d.cfg.DiskSize
		if size <= 0 {
			size = DefaultDiskSizeGB
		}
	}

	// For now, delegate to docker driver's volume creation
	// In the future, we could create DO block storage volumes for larger needs
	return d.dockerDriver.CreateVolume(ctx, name, size)
}

// Close either parks the machine (if reuse_worker=true) or deletes it and cleans up resources.
func (d *DigitalOcean) Close() error {
	ctx := context.Background()

	// If reuse_worker is enabled, park the machine instead of deleting it
	if d.reuseWorker && d.droplet != nil {
		return d.parkDroplet(ctx)
	}

	// Close docker driver first
	if d.dockerDriver != nil {
		if err := d.dockerDriver.Close(); err != nil {
			d.logger.Warn("digitalocean.docker.close.error", "err", err)
		}
	}

	// Close SSH client
	if d.sshClient != nil {
		if err := d.sshClient.Close(); err != nil {
			d.logger.Warn("digitalocean.ssh.close.error", "err", err)
		}
	}

	// Delete droplet
	if d.droplet != nil {
		d.logger.Info("digitalocean.droplet.delete", "id", d.droplet.ID)

		_, err := d.client.Droplets.Delete(ctx, d.droplet.ID)
		if err != nil {
			d.logger.Error("digitalocean.droplet.delete.error", "err", err)

			return fmt.Errorf("failed to delete droplet: %w", err)
		}

		d.logger.Info("digitalocean.droplet.delete.success", "id", d.droplet.ID)
	}

	// Delete SSH key from Digital Ocean
	if d.sshKeyID != 0 {
		_, err := d.client.Keys.DeleteByID(ctx, d.sshKeyID)
		if err != nil {
			d.logger.Warn("digitalocean.ssh_key.delete.remote_failed_on_close", "err", err)
		}
	}

	// Delete local SSH key file
	if d.sshKeyPath != "" {
		if err := os.Remove(d.sshKeyPath); err != nil && !os.IsNotExist(err) {
			d.logger.Warn("digitalocean.ssh_key.delete.local_failed", "err", err)
		}
	}

	return nil
}

// CleanupOrphanedResources deletes droplets and SSH keys matching the specified tag.
// If tag is empty, it defaults to "pocketci" which matches all PocketCI-created resources.
// For more targeted cleanup, use a specific tag like "pocketci-test" or a namespace tag.
// This is useful for cleaning up resources from failed or interrupted runs.
func CleanupOrphanedResources(ctx context.Context, token string, logger *slog.Logger, tag string) error {
	if tag == "" {
		tag = "pocketci"
	}

	client := godo.NewFromToken(token)

	// List all droplets with the specified tag
	droplets, _, err := client.Droplets.ListByTag(ctx, tag, &godo.ListOptions{PerPage: 200})
	if err != nil {
		return fmt.Errorf("failed to list droplets: %w", err)
	}

	for _, droplet := range droplets {
		logger.Info("digitalocean.cleanup.delete", "id", droplet.ID, "name", droplet.Name, "tag", tag)

		_, err := client.Droplets.Delete(ctx, droplet.ID)
		if err != nil {
			logger.Warn("digitalocean.cleanup.droplet.delete.error", "id", droplet.ID, "err", err)
		} else {
			logger.Info("digitalocean.cleanup.droplet.deleted.success", "id", droplet.ID)
		}
	}

	// List all SSH keys and delete those matching the tag pattern
	// SSH keys are named "pocketci-<namespace>" so we look for keys starting with the tag
	keyPrefix := tag + "-"
	if tag == "pocketci" {
		keyPrefix = "pocketci-"
	}

	keys, _, err := client.Keys.List(ctx, &godo.ListOptions{PerPage: 200})
	if err != nil {
		return fmt.Errorf("failed to list SSH keys: %w", err)
	}

	for _, key := range keys {
		if strings.HasPrefix(key.Name, keyPrefix) {
			logger.Info("digitalocean.cleanup.ssh_key.delete", "id", key.ID, "name", key.Name)

			_, err := client.Keys.DeleteByID(ctx, key.ID)
			if err != nil {
				logger.Warn("digitalocean.cleanup.ssh_key.delete.error", "id", key.ID, "err", err)
			} else {
				logger.Info("digitalocean.cleanup.ssh_key.delete.success", "id", key.ID)
			}
		}
	}

	return nil
}

var _ orchestra.Driver = &DigitalOcean{}
