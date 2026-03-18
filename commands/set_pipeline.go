package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/jtarchie/pocketci/backwards"
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
	Pipeline      string   `arg:""                  help:"Path to pipeline file (JS, TS, or YAML)"  required:"" type:"existingfile"`
	Name          string   `help:"Name for the pipeline (defaults to filename without extension)" short:"n"`
	ServerURL     string   `env:"CI_SERVER_URL"      help:"URL of the CI server"                                           required:"" short:"s"`
	Driver        string   `env:"CI_DRIVER"          help:"Orchestrator driver (e.g., 'docker', 'native', 'k8s')"      short:"d"`
	WebhookSecret string   `env:"CI_WEBHOOK_SECRET"  help:"Secret for webhook signature validation"                        short:"w"`
	Secret        []string `help:"Set a pipeline-scoped secret as KEY=VALUE (can be repeated)" short:"e"`
	SecretFile    string   `help:"Path to a file containing secrets in KEY=VALUE format (one per line)" type:"existingfile"`
	Resume        bool     `help:"Enable automatic resume for this pipeline" default:"false"`
	RBAC          string   `name:"rbac" help:"RBAC expression to control access to this pipeline (expr-lang)" env:"CI_PIPELINE_RBAC"`
	AuthToken     string   `env:"CI_AUTH_TOKEN"      help:"Bearer token for OAuth-authenticated servers"                   short:"t"`
	ConfigFile    string   `env:"CI_AUTH_CONFIG"     help:"Path to auth config file (default: ~/.pocketci/auth.config)"   short:"c"`

	// Driver-specific configuration (passed via driver_config map)
	DockerHost              string `name:"docker-host"              env:"CI_DOCKER_HOST"              help:"Docker daemon host URL"`
	HetznerToken            string `name:"hetzner-token"            env:"CI_HETZNER_TOKEN"            help:"Hetzner Cloud API token"`
	HetznerImage            string `name:"hetzner-image"            env:"CI_HETZNER_IMAGE"            help:"Hetzner server image"`
	HetznerServerType       string `name:"hetzner-server-type"      env:"CI_HETZNER_SERVER_TYPE"      help:"Hetzner server type"`
	HetznerLocation         string `name:"hetzner-location"         env:"CI_HETZNER_LOCATION"         help:"Hetzner datacenter location"`
	HetznerMaxWorkers       int    `name:"hetzner-max-workers"      env:"CI_HETZNER_MAX_WORKERS"      help:"Max concurrent Hetzner servers"`
	HetznerReuseWorker      bool   `name:"hetzner-reuse-worker"     env:"CI_HETZNER_REUSE_WORKER"     help:"Reuse idle Hetzner servers"`
	DigitalOceanToken       string `name:"digitalocean-token"       env:"CI_DIGITALOCEAN_TOKEN"       help:"DigitalOcean API token"`
	DigitalOceanImage       string `name:"digitalocean-image"       env:"CI_DIGITALOCEAN_IMAGE"       help:"Droplet image slug"`
	DigitalOceanSize        string `name:"digitalocean-size"        env:"CI_DIGITALOCEAN_SIZE"        help:"Droplet size slug"`
	DigitalOceanRegion      string `name:"digitalocean-region"      env:"CI_DIGITALOCEAN_REGION"      help:"Droplet region"`
	DigitalOceanMaxWorkers  int    `name:"digitalocean-max-workers" env:"CI_DIGITALOCEAN_MAX_WORKERS" help:"Max concurrent droplets"`
	DigitalOceanReuseWorker bool   `name:"digitalocean-reuse-worker" env:"CI_DIGITALOCEAN_REUSE_WORKER" help:"Reuse idle droplets"`
	FlyToken                string `name:"fly-token"                env:"CI_FLY_TOKEN"                help:"Fly.io API token"`
	FlyApp                  string `name:"fly-app"                  env:"CI_FLY_APP"                  help:"Fly.io app name"`
	FlyRegion               string `name:"fly-region"               env:"CI_FLY_REGION"               help:"Fly.io machine region"`
	FlyOrg                  string `name:"fly-org"                  env:"CI_FLY_ORG"                  help:"Fly.io org slug"`
	FlySize                 string `name:"fly-size"                 env:"CI_FLY_SIZE"                 help:"Fly.io machine size"`
	K8sKubeconfig           string `name:"k8s-kubeconfig"           env:"CI_K8S_KUBECONFIG"           help:"Path to kubeconfig file"`
	K8sNamespace            string `name:"k8s-namespace"            env:"CI_K8S_NAMESPACE"            help:"Kubernetes namespace for jobs"`
	QEMUMemory              string `name:"qemu-memory"              env:"CI_QEMU_MEMORY"              help:"QEMU VM memory"`
	QEMUCPUs                string `name:"qemu-cpus"                env:"CI_QEMU_CPUS"                help:"QEMU VM CPU count"`
	QEMUAccel               string `name:"qemu-accel"               env:"CI_QEMU_ACCEL"               help:"QEMU acceleration mode"`
	QEMUImage               string `name:"qemu-image"               env:"CI_QEMU_IMAGE"               help:"QEMU boot image path or URL"`
	QEMUBinary              string `name:"qemu-binary"              env:"CI_QEMU_BINARY"              help:"Path to qemu-system binary"`
	QEMUCacheDir            string `name:"qemu-cache-dir"           env:"CI_QEMU_CACHE_DIR"           help:"Directory for QEMU image cache"`
}

// pipelineRequest matches the server's expected JSON body for PUT /api/pipelines/:name.
type pipelineRequest struct {
	Content        string            `json:"content"`
	ContentType    string            `json:"content_type"`
	Driver         string            `json:"driver"`
	DriverConfig   json.RawMessage   `json:"driver_config,omitempty"`
	WebhookSecret  string            `json:"webhook_secret"`
	Secrets        map[string]string `json:"secrets,omitempty"`
	ResumeEnabled  *bool             `json:"resume_enabled,omitempty"`
	RBACExpression *string           `json:"rbac_expression,omitempty"`
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

	// Read the pipeline file
	content, err := os.ReadFile(c.Pipeline)
	if err != nil {
		return fmt.Errorf("could not read pipeline file: %w", err)
	}

	// Determine the file type and process accordingly
	ext := strings.ToLower(filepath.Ext(c.Pipeline))

	var contentType string

	switch ext {
	case ".yml", ".yaml":
		// Validate YAML structure and semantics, but do NOT transpile.
		// Transpilation happens lazily at pipeline trigger time so that
		// the latest pipeline_runner.ts bundle is always used.
		logger.Info("pipeline.validate")

		if err := backwards.ValidatePipeline(content); err != nil {
			return fmt.Errorf("pipeline validation failed: %w", err)
		}

		contentType = "yaml"

	case ".ts":
		// TypeScript — validate JS syntax before upload.
		logger.Info("pipeline.validate")

		_, err = runtime.TranspileAndValidate(string(content))
		if err != nil {
			return fmt.Errorf("pipeline validation failed: %w", err)
		}

		contentType = "ts"

	case ".js":
		// JavaScript — validate JS syntax before upload.
		logger.Info("pipeline.validate")

		_, err = runtime.TranspileAndValidate(string(content))
		if err != nil {
			return fmt.Errorf("pipeline validation failed: %w", err)
		}

		contentType = "js"

	default:
		return fmt.Errorf("unsupported file extension %q: expected .js, .ts, .yml, or .yaml", ext)
	}

	logger.Info("pipeline.validate.success")

	// Parse secrets from --secret-file and --secret flags
	secretsMap, err := c.parseSecrets()
	if err != nil {
		return err
	}

	// Upload to server via PUT /api/pipelines/:name
	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	endpoint := serverURL + "/api/pipelines/" + url.PathEscape(name)

	logger.Info("pipeline.upload", "url", redactURL(endpoint))

	reqBody := pipelineRequest{
		Content:       string(content),
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

	client := resty.New()

	// Extract basic auth from URL if present and strip it from the endpoint.
	if parsed, err := url.Parse(serverURL); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		client.SetBasicAuth(parsed.User.Username(), password)
		parsed.User = nil
		endpoint = parsed.String() + "/api/pipelines/" + url.PathEscape(name)
	}

	// Resolve auth token: explicit flag > config file lookup.
	token := ResolveAuthToken(c.AuthToken, c.ConfigFile, c.ServerURL)
	if token != "" {
		client.SetAuthToken(token)
	}

	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(reqBody).
		Put(endpoint)
	if err != nil {
		return fmt.Errorf("could not connect to server: %w", err)
	}

	body := resp.Body()

	switch resp.StatusCode() {
	case 200:
		// success — handled below
	case 401:
		return authRequiredError(serverURL)
	case 403:
		return accessDeniedError(serverURL)
	default:
		var errResp map[string]string
		if json.Unmarshal(body, &errResp) == nil {
			if msg, ok := errResp["error"]; ok {
				return fmt.Errorf("server error (%d): %s", resp.StatusCode(), msg)
			}
		}

		return fmt.Errorf("server error (%d): %s", resp.StatusCode(), string(body))
	}

	// Parse the successful response
	var pipeline storage.Pipeline
	if err := json.Unmarshal(body, &pipeline); err != nil {
		return fmt.Errorf("could not parse response: %w", err)
	}

	logger.Info("pipeline.upload.success",
		"id", pipeline.ID,
		"name", pipeline.Name,
	)

	fmt.Printf("Pipeline '%s' uploaded successfully!\n", pipeline.Name)
	fmt.Printf("  ID: %s\n", pipeline.ID)

	displayURL := c.ServerURL
	if parsed, err := url.Parse(c.ServerURL); err == nil && parsed.User != nil {
		parsed.User = nil
		displayURL = parsed.String()
	}

	fmt.Printf("  Server: %s\n", displayURL)

	if c.Driver != "" {
		fmt.Printf("  Driver: %s\n", c.Driver)
	}

	if len(secretsMap) > 0 {
		fmt.Printf("  Secrets: %d key(s) set\n", len(secretsMap))
	}

	if c.WebhookSecret != "" {
		fmt.Printf("  Webhook URL: %s/api/webhooks/%s\n", displayURL, pipeline.ID)
	}

	return nil
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
