package fly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	fly "github.com/superfly/fly-go"
	"github.com/superfly/fly-go/flaps"
	"github.com/superfly/fly-go/tokens"

	"github.com/jtarchie/pocketci/orchestra"
)

// ServerConfig holds server-level configuration for the Fly.io driver.
type ServerConfig struct {
	Token  string `json:"token,omitempty"`   // Fly.io API token (required)
	App    string `json:"app,omitempty"`     // Fly.io app name; if empty, an ephemeral app is created
	Region string `json:"region,omitempty"`  // Fly.io machine region
	Org    string `json:"org,omitempty"`     // Fly.io org slug (default: "personal")
	Size   string `json:"size,omitempty"`    // Fly.io machine size (default: "shared-cpu-1x")
	DiskGB int    `json:"disk_gb,omitempty"` // Workspace volume size in GB (default: 10)
}

// DriverName implements orchestra.DriverConfig.
func (ServerConfig) DriverName() string { return "fly" }

// Config holds the full configuration for the Fly.io driver.
type Config struct {
	ServerConfig
	Namespace string // Per-execution namespace identifier
}

type Fly struct {
	client    *flaps.Client
	apiClient *fly.Client
	logger    *slog.Logger
	namespace string
	appName   string
	region    string
	size      string
	diskGB    int // workspace volume size in GB
	org       string
	token     string // raw API token, needed for SSH cert/WireGuard operations

	// ephemeralApp is true if we created the app and should delete it on Close()
	ephemeralApp bool

	// Track resources for cleanup
	mu         sync.Mutex
	machineIDs []string
	volumeIDs  []string

	// Shared workspace volume: all logical mounts share a single physical Fly
	// volume (Fly machines only support 1 volume). Each mount name becomes a
	// subdirectory under /workspace.
	sharedVolumeID    string             // physical Fly volume ID (empty until first CreateVolume)
	volumes           map[string]*Volume // mount name → logical Volume
	volumeAttachments map[string]string  // volume ID → machine ID

	// helperMachines tracks persistent suspended helper machines (volume ID → machine ID)
	// so they can be resumed quickly instead of cold-booting on each use.
	helperMachines map[string]string
}

func New(ctx context.Context, cfg Config, logger *slog.Logger) (orchestra.Driver, error) {
	if cfg.Token == "" {
		return nil, errors.New("fly driver requires a token (set via CI_FLY_TOKEN)")
	}

	org := cfg.Org
	if org == "" {
		org = "personal"
	}

	size := cfg.Size
	if size == "" {
		size = "shared-cpu-1x"
	}

	toks := tokens.Parse(cfg.Token)

	// Discharge third-party caveats on macaroon tokens. Macaroon tokens have
	// short-lived discharge tokens that need refreshing via auth.fly.io.
	_, tokErr := toks.Update(ctx)
	if tokErr != nil {
		logger.Warn("fly.tokens.update", "err", tokErr)
	}

	fly.SetBaseURL("https://api.fly.io")

	apiClient := fly.NewClientFromOptions(fly.ClientOptions{
		Tokens: toks,
		Name:   "pocketci",
	})

	client, err := flaps.NewWithOptions(ctx, flaps.NewClientOpts{
		Tokens: toks,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create fly client: %w", err)
	}

	f := &Fly{
		client:            client,
		apiClient:         apiClient,
		logger:            logger,
		namespace:         cfg.Namespace,
		region:            cfg.Region,
		size:              size,
		diskGB:            cfg.DiskGB,
		org:               org,
		token:             cfg.Token,
		volumes:           make(map[string]*Volume),
		volumeAttachments: make(map[string]string),
		helperMachines:    make(map[string]string),
	}

	appName := cfg.App

	// If no app name provided, create an ephemeral one
	if appName == "" {
		appName = SanitizeAppName("pocketci-" + cfg.Namespace)

		logger.Info("fly.app.create", "app", appName, "org", org)

		_, err := client.CreateApp(ctx, flaps.CreateAppRequest{
			Name: appName,
			Org:  org,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create fly app %q: %w", appName, err)
		}

		err = client.WaitForApp(ctx, appName)
		if err != nil {
			return nil, fmt.Errorf("failed waiting for fly app %q to be ready: %w", appName, err)
		}

		f.ephemeralApp = true
	}

	f.appName = appName

	return f, nil
}

func (f *Fly) Name() string {
	return "fly"
}

func (f *Fly) Close() error {
	f.mu.Lock()
	machineIDs := make([]string, len(f.machineIDs))
	copy(machineIDs, f.machineIDs)
	volumeIDs := make([]string, len(f.volumeIDs))
	copy(volumeIDs, f.volumeIDs)
	helperMachineIDs := make([]string, 0, len(f.helperMachines))
	for _, machineID := range f.helperMachines {
		helperMachineIDs = append(helperMachineIDs, machineID)
	}
	f.mu.Unlock()

	ctx := context.Background()

	// Truly destroy persistent helper machines so their volumes can be deleted
	for _, machineID := range helperMachineIDs {
		f.logger.Debug("fly.helper.destroy", "machine", machineID)

		_ = f.client.Kill(ctx, f.appName, machineID)

		machine := &fly.Machine{ID: machineID}
		_ = f.client.Wait(ctx, f.appName, machine.ID, flaps.WithWaitStates("stopped"), flaps.WithWaitTimeout(30*time.Second))

		_ = f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
			ID:   machineID,
			Kill: true,
		}, "")
	}

	// Destroy all tracked machines
	for _, machineID := range machineIDs {
		f.logger.Debug("fly.machine.destroy", "machine", machineID)

		err := f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
			ID:   machineID,
			Kill: true,
		}, "")
		if err != nil {
			f.logger.Warn("fly.machine.destroy.error", "machine", machineID, "err", err)
		}
	}

	// Best-effort sweep for untracked machines from partial/failed launches.
	f.sweepUntrackedMachines(ctx)

	// Delete all tracked volumes
	for _, volumeID := range volumeIDs {
		f.logger.Debug("fly.volume.delete", "volume", volumeID)

		_, err := f.client.DeleteVolume(ctx, f.appName, volumeID)
		if err != nil {
			f.logger.Warn("fly.volume.delete.error", "volume", volumeID, "err", err)
		}
	}

	// If we created the app, delete it
	if f.ephemeralApp {
		f.logger.Info("fly.app.delete", "app", f.appName)

		err := f.client.DeleteApp(ctx, f.appName)
		if err != nil {
			return fmt.Errorf("failed to delete fly app %q: %w", f.appName, err)
		}
	}

	return nil
}

func (f *Fly) trackMachine(machineID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.machineIDs = append(f.machineIDs, machineID)
}

// sweepUntrackedMachines destroys any machines belonging to this namespace
// that were not explicitly tracked (e.g. from partial/failed launches).
func (f *Fly) sweepUntrackedMachines(ctx context.Context) {
	machines, err := f.client.List(ctx, f.appName, "")
	if err != nil {
		f.logger.Warn("fly.machine.list.error", "app", f.appName, "err", err)

		return
	}

	namespacePrefix := SanitizeAppName(f.namespace) + "-"

	for _, machine := range machines {
		if machine == nil {
			continue
		}

		if machine.State == "destroyed" {
			continue
		}

		machineNamespace := ""
		if machine.Config != nil && machine.Config.Metadata != nil {
			machineNamespace = machine.Config.Metadata["orchestra.namespace"]
		}

		if machineNamespace != f.namespace && !strings.HasPrefix(machine.Name, namespacePrefix) {
			continue
		}

		f.logger.Debug("fly.machine.destroy.sweep", "machine", machine.ID, "name", machine.Name, "state", machine.State)

		err = f.client.Destroy(ctx, f.appName, fly.RemoveMachineInput{
			ID:   machine.ID,
			Kill: true,
		}, "")
		if err != nil {
			f.logger.Warn("fly.machine.destroy.sweep.error", "machine", machine.ID, "err", err)
		}
	}
}

func (f *Fly) trackVolume(volumeID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.volumeIDs = append(f.volumeIDs, volumeID)
}

// Client returns the underlying Flaps client for advanced operations.
func (f *Fly) Client() *flaps.Client { return f.client }

// AppName returns the Fly app name used by this driver instance.
func (f *Fly) AppName() string { return f.appName }

// IsTrackedMachine reports whether the given machine ID is in the driver's cleanup list.
func (f *Fly) IsTrackedMachine(machineID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, id := range f.machineIDs {
		if id == machineID {
			return true
		}
	}

	return false
}

// SanitizeAppName ensures a Fly app name conforms to Fly's requirements:
// under 63 chars, only lowercase letters, numbers, and dashes.
func SanitizeAppName(name string) string {
	name = strings.ToLower(name)

	var b strings.Builder

	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}

	name = b.String()

	// Trim leading/trailing dashes
	name = strings.Trim(name, "-")

	// Collapse consecutive dashes
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}

	if len(name) > 63 {
		name = name[:63]
		name = strings.TrimRight(name, "-")
	}

	return name
}

// sanitizeVolumeName ensures a Fly volume name conforms to Fly's requirements:
// max 30 chars, only lowercase letters, numbers, and underscores.
func sanitizeVolumeName(name string) string {
	name = strings.ToLower(name)

	var b strings.Builder

	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}

	name = b.String()

	// Trim leading/trailing underscores
	name = strings.Trim(name, "_")

	// Collapse consecutive underscores
	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", "_")
	}

	if len(name) > 30 {
		name = name[:30]
		name = strings.TrimRight(name, "_")
	}

	return name
}

// shellescape wraps a string in single quotes for safe use in shell commands.
// Any embedded single quotes are escaped as '\" (end quote, escaped quote, start quote).
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// shelljoin quotes and joins a command slice into a single shell-safe string.
// Each argument is individually escaped so spaces and special characters are preserved.
func shelljoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellescape(a)
	}

	return strings.Join(quoted, " ")
}

var (
	_ orchestra.Driver          = &Fly{}
	_ orchestra.Container       = &Container{}
	_ orchestra.ContainerStatus = &containerStatus{}
	_ orchestra.Volume          = &Volume{}
)
