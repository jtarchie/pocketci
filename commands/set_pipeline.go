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
	"github.com/jtarchie/pocketci/runtime"
	secretsPkg "github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

type SetPipeline struct {
	Pipeline      string   `arg:""                  help:"Path to pipeline file (JS, TS, or YAML)"  required:"" type:"existingfile"`
	Name          string   `help:"Name for the pipeline (defaults to filename without extension)" short:"n"`
	ServerURL     string   `env:"CI_SERVER_URL"      help:"URL of the CI server"                                           required:"" short:"s"`
	Driver        string   `env:"CI_DRIVER"          help:"Orchestrator driver DSN (e.g., 'docker', 'native', 'k8s')"      short:"d"`
	WebhookSecret string   `env:"CI_WEBHOOK_SECRET"  help:"Secret for webhook signature validation"                        short:"w"`
	Secret        []string `help:"Set a pipeline-scoped secret as KEY=VALUE (can be repeated)" short:"e"`
	SecretFile    string   `help:"Path to a file containing secrets in KEY=VALUE format (one per line)" type:"existingfile"`
	Resume        bool     `help:"Enable automatic resume for this pipeline" default:"false"`
	RBAC          string   `help:"RBAC expression to control access to this pipeline (expr-lang)" env:"CI_PIPELINE_RBAC"`
	AuthToken     string   `env:"CI_AUTH_TOKEN"      help:"Bearer token for OAuth-authenticated servers"                   short:"t"`
	ConfigFile    string   `env:"CI_AUTH_CONFIG"     help:"Path to auth config file (default: ~/.pocketci/auth.config)"   short:"c"`
}

// pipelineRequest matches the server's expected JSON body for PUT /api/pipelines/:name.
type pipelineRequest struct {
	Content        string            `json:"content"`
	ContentType    string            `json:"content_type"`
	DriverDSN      string            `json:"driver_dsn"`
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
		DriverDSN:     c.Driver,
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
