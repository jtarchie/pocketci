package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/client"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/digitalocean"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/fly"
	"github.com/jtarchie/pocketci/orchestra/hetzner"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/orchestra/qemu"
	"github.com/jtarchie/pocketci/runtime"
	secretsPkg "github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

type SetPipeline struct {
	ServerConfig
	Pipeline      string   `arg:""                                                                      help:"Path to pipeline file (JS, TS, or YAML)"                        required:"" type:"existingfile"`
	Name          string   `help:"Name for the pipeline (defaults to filename without extension)"       short:"n"`
	Driver        string   `env:"CI_DRIVER"                                                             help:"Orchestrator driver (e.g., 'docker', 'native', 'k8s')"          short:"d"`
	WebhookSecret string   `env:"CI_WEBHOOK_SECRET"                                                     help:"Secret for webhook signature validation"                        short:"w"`
	Secret        []string `help:"Set a pipeline-scoped secret as KEY=VALUE (can be repeated)"          short:"e"`
	SecretFile    string   `help:"Path to a file containing secrets in KEY=VALUE format (one per line)" type:"existingfile"`
	Resume        bool     `default:"false"                                                             help:"Enable automatic resume for this pipeline"`
	RBAC          string   `env:"CI_PIPELINE_RBAC"                                                      help:"RBAC expression to control access to this pipeline (expr-lang)" name:"rbac"`

	// Driver-specific configuration (passed via driver_config map)
	DockerHost              string `env:"CI_DOCKER_HOST"               help:"Docker daemon host URL"         name:"docker-host"`
	HetznerToken            string `env:"CI_HETZNER_TOKEN"             help:"Hetzner Cloud API token"        name:"hetzner-token"`
	HetznerImage            string `env:"CI_HETZNER_IMAGE"             help:"Hetzner server image"           name:"hetzner-image"`
	HetznerServerType       string `env:"CI_HETZNER_SERVER_TYPE"       help:"Hetzner server type"            name:"hetzner-server-type"`
	HetznerLocation         string `env:"CI_HETZNER_LOCATION"          help:"Hetzner datacenter location"    name:"hetzner-location"`
	HetznerMaxWorkers       int    `env:"CI_HETZNER_MAX_WORKERS"       help:"Max concurrent Hetzner servers" name:"hetzner-max-workers"`
	HetznerReuseWorker      bool   `env:"CI_HETZNER_REUSE_WORKER"      help:"Reuse idle Hetzner servers"     name:"hetzner-reuse-worker"`
	DigitalOceanToken       string `env:"CI_DIGITALOCEAN_TOKEN"        help:"DigitalOcean API token"         name:"digitalocean-token"`
	DigitalOceanImage       string `env:"CI_DIGITALOCEAN_IMAGE"        help:"Droplet image slug"             name:"digitalocean-image"`
	DigitalOceanSize        string `env:"CI_DIGITALOCEAN_SIZE"         help:"Droplet size slug"              name:"digitalocean-size"`
	DigitalOceanRegion      string `env:"CI_DIGITALOCEAN_REGION"       help:"Droplet region"                 name:"digitalocean-region"`
	DigitalOceanMaxWorkers  int    `env:"CI_DIGITALOCEAN_MAX_WORKERS"  help:"Max concurrent droplets"        name:"digitalocean-max-workers"`
	DigitalOceanReuseWorker bool   `env:"CI_DIGITALOCEAN_REUSE_WORKER" help:"Reuse idle droplets"            name:"digitalocean-reuse-worker"`
	FlyToken                string `env:"CI_FLY_TOKEN"                 help:"Fly.io API token"               name:"fly-token"`
	FlyApp                  string `env:"CI_FLY_APP"                   help:"Fly.io app name"                name:"fly-app"`
	FlyRegion               string `env:"CI_FLY_REGION"                help:"Fly.io machine region"          name:"fly-region"`
	FlyOrg                  string `env:"CI_FLY_ORG"                   help:"Fly.io org slug"                name:"fly-org"`
	FlySize                 string `env:"CI_FLY_SIZE"                  help:"Fly.io machine size"            name:"fly-size"`
	K8sKubeconfig           string `env:"CI_K8S_KUBECONFIG"            help:"Path to kubeconfig file"        name:"k8s-kubeconfig"`
	K8sNamespace            string `env:"CI_K8S_NAMESPACE"             help:"Kubernetes namespace for jobs"  name:"k8s-namespace"`
	QEMUMemory              string `env:"CI_QEMU_MEMORY"               help:"QEMU VM memory"                 name:"qemu-memory"`
	QEMUCPUs                string `env:"CI_QEMU_CPUS"                 help:"QEMU VM CPU count"              name:"qemu-cpus"`
	QEMUAccel               string `env:"CI_QEMU_ACCEL"                help:"QEMU acceleration mode"         name:"qemu-accel"`
	QEMUImage               string `env:"CI_QEMU_IMAGE"                help:"QEMU boot image path or URL"    name:"qemu-image"`
	QEMUBinary              string `env:"CI_QEMU_BINARY"               help:"Path to qemu-system binary"     name:"qemu-binary"`
	QEMUCacheDir            string `env:"CI_QEMU_CACHE_DIR"            help:"Directory for QEMU image cache" name:"qemu-cache-dir"`

	// Output is the writer for user-facing messages (defaults to os.Stdout).
	Output io.Writer `kong:"-"`
}

func (c *SetPipeline) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("pipeline.set")

	// Determine pipeline name from filename if not provided
	name := c.Name
	if name == "" {
		base := filepath.Base(c.Pipeline)
		ext := filepath.Ext(base)
		name = strings.TrimSuffix(base, ext)
	}

	logger.Info("pipeline.read", "file", c.Pipeline, "name", name)

	content, err := os.ReadFile(c.Pipeline)
	if err != nil {
		return fmt.Errorf("could not read pipeline file: %w", err)
	}

	contentType, err := c.validatePipelineContent(logger, content)
	if err != nil {
		return err
	}

	logger.Info("pipeline.validate.success")

	secretsMap, err := c.parseSecrets()
	if err != nil {
		return err
	}

	reqBody := c.buildRequestBody(string(content), contentType, secretsMap)

	apiClient := c.NewClient()

	logger.Info("pipeline.upload", "url", RedactURL(apiClient.ServerURL()+"/api/pipelines/"+url.PathEscape(name)))

	pipeline, err := apiClient.SetPipeline(name, reqBody)
	if err != nil {
		return err
	}

	logger.Info("pipeline.upload.success", "id", pipeline.ID, "name", pipeline.Name)

	c.printSuccess(pipeline, secretsMap)

	return nil
}

func (c *SetPipeline) validatePipelineContent(logger *slog.Logger, content []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(c.Pipeline))

	switch ext {
	case ".yml", ".yaml":
		logger.Info("pipeline.validate")

		if err := backwards.ValidatePipeline(content); err != nil {
			return "", fmt.Errorf("pipeline validation failed: %w", err)
		}

		return "yaml", nil

	case ".ts":
		logger.Info("pipeline.validate")

		if _, err := runtime.TranspileAndValidate(string(content)); err != nil {
			return "", fmt.Errorf("pipeline validation failed: %w", err)
		}

		return "ts", nil

	case ".js":
		logger.Info("pipeline.validate")

		if _, err := runtime.TranspileAndValidate(string(content)); err != nil {
			return "", fmt.Errorf("pipeline validation failed: %w", err)
		}

		return "js", nil

	default:
		return "", fmt.Errorf("unsupported file extension %q: expected .js, .ts, .yml, or .yaml", ext)
	}
}

func (c *SetPipeline) buildRequestBody(content, contentType string, secretsMap map[string]string) client.SetPipelineRequest {
	reqBody := client.SetPipelineRequest{
		Content:       content,
		ContentType:   contentType,
		Driver:        c.Driver,
		DriverConfig:  c.buildDriverConfig(),
		WebhookSecret: c.WebhookSecret,
		Secrets:       secretsMap,
		ResumeEnabled: &c.Resume,
	}

	if c.RBAC != "" {
		reqBody.RBACExpression = &c.RBAC
	}

	return reqBody
}

func (c *SetPipeline) output() io.Writer {
	if c.Output != nil {
		return c.Output
	}

	return os.Stdout
}

func (c *SetPipeline) printSuccess(pipeline storage.Pipeline, secretsMap map[string]string) {
	w := c.output()

	_, _ = fmt.Fprintf(w, "Pipeline '%s' uploaded successfully!\n", pipeline.Name)
	_, _ = fmt.Fprintf(w, "  ID: %s\n", pipeline.ID)

	displayURL := c.ServerURL
	if parsed, err := url.Parse(c.ServerURL); err == nil && parsed.User != nil {
		parsed.User = nil
		displayURL = parsed.String()
	}

	_, _ = fmt.Fprintf(w, "  URL: %s/pipelines/%s/\n", displayURL, pipeline.ID)
	_, _ = fmt.Fprintf(w, "  Server: %s\n", displayURL)

	if c.Driver != "" {
		_, _ = fmt.Fprintf(w, "  Driver: %s\n", c.Driver)
	}

	if len(secretsMap) > 0 {
		_, _ = fmt.Fprintf(w, "  Secrets: %d key(s) set\n", len(secretsMap))
	}

	if c.WebhookSecret != "" {
		_, _ = fmt.Fprintf(w, "  Webhook URL: %s/api/webhooks/%s\n", displayURL, pipeline.ID)
	}
}

// parseSecrets merges secrets from --secret-file and --secret flags.
// Flag values take precedence over file values on key collision.
func (c *SetPipeline) parseSecrets() (map[string]string, error) {
	result := make(map[string]string)

	// Parse --secret-file first (lower priority)
	if c.SecretFile != "" {
		f, err := os.Open(c.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("could not open secret file: %w", err)
		}
		defer func() { _ = f.Close() }()

		scanner := bufio.NewScanner(f)
		lineNum := 0

		for scanner.Scan() {
			lineNum++
			line := strings.TrimSpace(scanner.Text())

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			key, value, found := parseSecretFlag(line)
			if !found {
				return nil, fmt.Errorf("invalid secret in file %q line %d: expected KEY=VALUE format, got %q", c.SecretFile, lineNum, line)
			}

			if secretsPkg.IsSystemKey(key) {
				return nil, fmt.Errorf("secret key %q in file %q line %d is reserved for system use", key, c.SecretFile, lineNum)
			}

			result[key] = value
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("could not read secret file: %w", err)
		}
	}

	// Parse --secret flags (higher priority, overwrite file values)
	for _, s := range c.Secret {
		key, value, found := parseSecretFlag(s)
		if !found {
			return nil, fmt.Errorf("invalid --secret flag %q: expected KEY=VALUE format", s)
		}

		if secretsPkg.IsSystemKey(key) {
			return nil, fmt.Errorf("secret key %q is reserved for system use and cannot be set via --secret", key)
		}

		result[key] = value
	}

	if len(result) == 0 {
		return nil, nil //nolint:nilnil
	}

	return result, nil
}

func parseSecretFlag(s string) (string, string, bool) {
	key, value, found := strings.Cut(s, "=")
	if !found || key == "" {
		return "", "", false
	}

	return key, value, true
}

// buildDriverConfig builds a typed driver config from the CLI flags and
// marshals it to JSON for the server. Returns nil if no config flags are set.
func (c *SetPipeline) buildDriverConfig() json.RawMessage {
	var cfg orchestra.DriverConfig

	switch c.Driver {
	case "docker":
		if c.DockerHost != "" {
			cfg = docker.ServerConfig{Host: c.DockerHost}
		}
	case "hetzner":
		if c.HetznerToken != "" {
			cfg = hetzner.ServerConfig{
				Token:       c.HetznerToken,
				Image:       c.HetznerImage,
				ServerType:  c.HetznerServerType,
				Location:    c.HetznerLocation,
				MaxWorkers:  c.HetznerMaxWorkers,
				ReuseWorker: c.HetznerReuseWorker,
			}
		}
	case "digitalocean":
		if c.DigitalOceanToken != "" {
			cfg = digitalocean.ServerConfig{
				Token:       c.DigitalOceanToken,
				Image:       c.DigitalOceanImage,
				Size:        c.DigitalOceanSize,
				Region:      c.DigitalOceanRegion,
				MaxWorkers:  c.DigitalOceanMaxWorkers,
				ReuseWorker: c.DigitalOceanReuseWorker,
			}
		}
	case "fly":
		if c.FlyToken != "" {
			cfg = fly.ServerConfig{
				Token:  c.FlyToken,
				App:    c.FlyApp,
				Region: c.FlyRegion,
				Org:    c.FlyOrg,
				Size:   c.FlySize,
			}
		}
	case "k8s":
		if c.K8sKubeconfig != "" {
			cfg = k8s.ServerConfig{
				Kubeconfig:   c.K8sKubeconfig,
				K8sNamespace: c.K8sNamespace,
			}
		}
	case "qemu":
		if c.QEMUImage != "" {
			cfg = qemu.ServerConfig{
				Memory:   c.QEMUMemory,
				CPUs:     c.QEMUCPUs,
				Accel:    c.QEMUAccel,
				Image:    c.QEMUImage,
				Binary:   c.QEMUBinary,
				CacheDir: c.QEMUCacheDir,
			}
		}
	}

	if cfg == nil {
		return nil
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}

	return raw
}
