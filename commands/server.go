package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/observability"
	"github.com/jtarchie/pocketci/observability/honeybadger"
	"github.com/jtarchie/pocketci/observability/posthog"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/cache"
	"github.com/jtarchie/pocketci/orchestra/digitalocean"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/fly"
	"github.com/jtarchie/pocketci/orchestra/hetzner"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/orchestra/qemu"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/labstack/echo/v5"
)

type Server struct {
	Port               int           `default:"8080"             env:"CI_PORT"                 help:"Port to run the server on"`
	Storage            string        `default:"sqlite://test.db" env:"CI_STORAGE"              help:"Path to storage file"                      required:""`
	MaxInFlight        int           `default:"10"               env:"CI_MAX_IN_FLIGHT"         help:"Maximum concurrent pipeline executions"`
	WebhookTimeout     time.Duration `default:"5s"               env:"CI_WEBHOOK_TIMEOUT"       help:"Timeout waiting for pipeline webhook response"`
	BasicAuth          string        `env:"CI_BASIC_AUTH"         help:"Basic auth credentials in format 'username:password' (optional)"`
	AllowedDrivers     string        `default:"*"                env:"CI_ALLOWED_DRIVERS"       help:"Comma-separated list of allowed driver names (e.g., 'docker,native,k8s'), or '*' for all"`
	AllowedFeatures    string        `default:"*"                env:"CI_ALLOWED_FEATURES"      help:"Comma-separated list of allowed features (webhooks,secrets,notifications,fetch,resume), or '*' for all"`
	FetchTimeout       time.Duration `default:"30s"              env:"CI_FETCH_TIMEOUT"         help:"Default timeout for fetch() calls in pipelines"`
	FetchMaxResponseMB int           `default:"10"               env:"CI_FETCH_MAX_RESPONSE_MB" help:"Maximum response body size in MB for fetch() calls"`
	Secrets            string        `default:"sqlite://test.db?key=testing"                 env:"CI_SECRETS"              help:"Secrets backend DSN (e.g., 'sqlite://secrets.db?key=my-passphrase')"`
	Secret             []string      `help:"Set a global secret as KEY=VALUE (can be repeated)" short:"e"`
	PosthogAPIKey      string        `env:"CI_POSTHOG_API_KEY"     help:"PostHog API key (e.g., 'phc_abc123')"`
	PosthogEndpoint    string        `env:"CI_POSTHOG_ENDPOINT"    help:"PostHog ingestion endpoint URL (defaults to PostHog cloud)"`
	HoneybadgerAPIKey  string        `env:"CI_HONEYBADGER_API_KEY" help:"Honeybadger API key"`
	HoneybadgerEnv     string        `env:"CI_HONEYBADGER_ENV"     help:"Honeybadger environment name (e.g., 'production')"`

	// OAuth provider configuration
	OAuthGithubClientID        string `env:"CI_OAUTH_GITHUB_CLIENT_ID"        help:"GitHub OAuth application client ID"`
	OAuthGithubClientSecret    string `env:"CI_OAUTH_GITHUB_CLIENT_SECRET"    help:"GitHub OAuth application client secret"`
	OAuthGitlabClientID        string `env:"CI_OAUTH_GITLAB_CLIENT_ID"        help:"GitLab OAuth application client ID"`
	OAuthGitlabClientSecret    string `env:"CI_OAUTH_GITLAB_CLIENT_SECRET"    help:"GitLab OAuth application client secret"`
	OAuthGitlabURL             string `env:"CI_OAUTH_GITLAB_URL"              help:"Self-hosted GitLab URL (defaults to https://gitlab.com)"`
	OAuthMicrosoftClientID     string `env:"CI_OAUTH_MICROSOFT_CLIENT_ID"     help:"Microsoft/Azure AD OAuth client ID"`
	OAuthMicrosoftClientSecret string `env:"CI_OAUTH_MICROSOFT_CLIENT_SECRET" help:"Microsoft/Azure AD OAuth client secret"`
	OAuthMicrosoftTenant       string `env:"CI_OAUTH_MICROSOFT_TENANT"        help:"Azure AD tenant ID (defaults to 'common')"`
	OAuthSessionSecret         string `env:"CI_OAUTH_SESSION_SECRET"          help:"Secret key for encrypting OAuth session cookies"`
	OAuthCallbackURL           string `env:"CI_OAUTH_CALLBACK_URL"            help:"Base URL for OAuth callbacks (e.g., 'https://ci.example.com')"`

	// RBAC configuration
	ServerRBAC string `env:"CI_SERVER_RBAC" help:"Expr expression for server-level access control (e.g., 'Email endsWith \"@company.com\"')"`

	// Docker driver
	DockerHost string `env:"CI_DOCKER_HOST" help:"Docker daemon host URL (e.g., 'tcp://host:2376', 'ssh://user@host')"`

	// Hetzner driver
	HetznerToken       string `env:"CI_HETZNER_TOKEN"        help:"Hetzner Cloud API token"`
	HetznerImage       string `env:"CI_HETZNER_IMAGE"        help:"Hetzner server image (default: docker-ce)"`
	HetznerServerType  string `env:"CI_HETZNER_SERVER_TYPE"  help:"Hetzner server type (default: cx23)"`
	HetznerLocation    string `env:"CI_HETZNER_LOCATION"     help:"Hetzner datacenter location (default: nbg1)"`
	HetznerMaxWorkers  int    `env:"CI_HETZNER_MAX_WORKERS"  help:"Max concurrent Hetzner servers (default: 1)"`
	HetznerReuseWorker bool   `env:"CI_HETZNER_REUSE_WORKER" help:"Reuse idle Hetzner servers across runs"`

	// DigitalOcean driver
	DigitalOceanToken       string `env:"CI_DIGITALOCEAN_TOKEN"        help:"DigitalOcean API token"`
	DigitalOceanImage       string `env:"CI_DIGITALOCEAN_IMAGE"        help:"Droplet image slug"`
	DigitalOceanSize        string `env:"CI_DIGITALOCEAN_SIZE"         help:"Droplet size slug"`
	DigitalOceanRegion      string `env:"CI_DIGITALOCEAN_REGION"       help:"Droplet region"`
	DigitalOceanMaxWorkers  int    `env:"CI_DIGITALOCEAN_MAX_WORKERS"  help:"Max concurrent droplets"`
	DigitalOceanReuseWorker bool   `env:"CI_DIGITALOCEAN_REUSE_WORKER" help:"Reuse idle droplets across runs"`

	// Fly.io driver
	FlyToken  string `env:"CI_FLY_TOKEN"  help:"Fly.io API token"`
	FlyApp    string `env:"CI_FLY_APP"    help:"Fly.io app name"`
	FlyRegion string `env:"CI_FLY_REGION" help:"Fly.io machine region"`
	FlyOrg    string `env:"CI_FLY_ORG"    help:"Fly.io org slug"`
	FlySize   string `env:"CI_FLY_SIZE"   help:"Fly.io machine size"`

	// Kubernetes driver
	K8sKubeconfig string `env:"CI_K8S_KUBECONFIG" help:"Path to kubeconfig file (uses in-cluster config if empty)"`
	K8sNamespace  string `env:"CI_K8S_NAMESPACE"  help:"Kubernetes namespace for jobs (default: default)"`

	// QEMU driver
	QEMUMemory   string `env:"CI_QEMU_MEMORY"    help:"QEMU VM memory (e.g., '2048')"`
	QEMUCPUs     string `env:"CI_QEMU_CPUS"      help:"QEMU VM CPU count"`
	QEMUAccel    string `env:"CI_QEMU_ACCEL"     help:"QEMU acceleration: hvf, kvm, tcg, or auto"`
	QEMUImage    string `env:"CI_QEMU_IMAGE"     help:"QEMU boot image path or URL"`
	QEMUBinary   string `env:"CI_QEMU_BINARY"    help:"Path to qemu-system binary"`
	QEMUCacheDir string `env:"CI_QEMU_CACHE_DIR" help:"Directory for QEMU image cache"`

	// Cache (optional, wraps the driver)
	CacheURL         string `env:"CI_CACHE_URL"         help:"Cache store URL (e.g., 's3://bucket/prefix?region=us-east-1')"`
	CacheCompression string `env:"CI_CACHE_COMPRESSION" help:"Cache compression: zstd, gzip, or none (default: zstd)"`
	CachePrefix      string `env:"CI_CACHE_PREFIX"      help:"Cache key prefix"`
}

func (c *Server) Run(logger *slog.Logger) error {
	// Initialize observability provider if configured
	var obsProvider observability.Provider

	switch {
	case c.PosthogAPIKey != "":
		var err error

		obsProvider, err = posthog.New(posthog.Config{
			APIKey:   c.PosthogAPIKey,
			Endpoint: c.PosthogEndpoint,
		}, logger)
		if err != nil {
			return fmt.Errorf("could not create posthog provider: %w", err)
		}

		defer func() { _ = obsProvider.Close() }()

	case c.HoneybadgerAPIKey != "":
		var err error

		obsProvider, err = honeybadger.New(honeybadger.Config{
			APIKey: c.HoneybadgerAPIKey,
			Env:    c.HoneybadgerEnv,
		}, logger)
		if err != nil {
			return fmt.Errorf("could not create honeybadger provider: %w", err)
		}

		defer func() { _ = obsProvider.Close() }()
	}

	if obsProvider != nil {
		// Wrap the logger so log records are also forwarded to the provider
		logger = slog.New(obsProvider.SlogHandler(logger.Handler()))
		slog.SetDefault(logger)
	}

	initStorage, found := storage.GetFromDSN(c.Storage)
	if !found {
		return fmt.Errorf("could not get storage driver: %w", errors.ErrUnsupported)
	}

	client, err := initStorage(c.Storage, "", logger)
	if err != nil {
		return fmt.Errorf("could not create sqlite client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Initialize secrets manager if configured
	var secretsManager secrets.Manager

	if c.Secrets != "" {
		secretsManager, err = secrets.GetFromDSN(c.Secrets, logger)
		if err != nil {
			return fmt.Errorf("could not create secrets manager: %w", err)
		}
		defer func() { _ = secretsManager.Close() }()

		// Store any secrets provided via --secret flags (global scope)
		for _, s := range c.Secret {
			key, value, found := strings.Cut(s, "=")
			if !found || key == "" {
				return fmt.Errorf("invalid --secret flag %q: expected KEY=VALUE format", s)
			}

			err = secretsManager.Set(context.Background(), secrets.GlobalScope, key, value)
			if err != nil {
				return fmt.Errorf("could not set global secret %q: %w", key, err)
			}
		}
	}

	// Build auth config from OAuth flags
	authConfig := &auth.Config{
		GithubClientID:        c.OAuthGithubClientID,
		GithubClientSecret:    c.OAuthGithubClientSecret,
		GitlabClientID:        c.OAuthGitlabClientID,
		GitlabClientSecret:    c.OAuthGitlabClientSecret,
		GitlabURL:             c.OAuthGitlabURL,
		MicrosoftClientID:     c.OAuthMicrosoftClientID,
		MicrosoftClientSecret: c.OAuthMicrosoftClientSecret,
		MicrosoftTenant:       c.OAuthMicrosoftTenant,
		SessionSecret:         c.OAuthSessionSecret,
		CallbackURL:           c.OAuthCallbackURL,
		ServerRBAC:            c.ServerRBAC,
	}

	// Parse basic auth credentials if provided
	var basicAuthUsername, basicAuthPassword string
	if c.BasicAuth != "" {
		parts := strings.SplitN(c.BasicAuth, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid basic auth format: expected 'username:password', got '%s'", c.BasicAuth)
		}
		basicAuthUsername = parts[0]
		basicAuthPassword = parts[1]
		if basicAuthUsername == "" || basicAuthPassword == "" {
			return fmt.Errorf("basic auth username and password cannot be empty")
		}
	}

	// Validate mutual exclusion: basic auth and OAuth cannot coexist
	if c.BasicAuth != "" && authConfig.HasOAuthProviders() {
		return fmt.Errorf("basic auth and OAuth providers cannot be used together: choose one authentication method")
	}

	// Validate OAuth config: session secret required when providers are configured
	if authConfig.HasOAuthProviders() && authConfig.SessionSecret == "" {
		return fmt.Errorf("CI_OAUTH_SESSION_SECRET is required when OAuth providers are configured")
	}

	if authConfig.HasOAuthProviders() && authConfig.CallbackURL == "" {
		return fmt.Errorf("CI_OAUTH_CALLBACK_URL is required when OAuth providers are configured")
	}

	// Validate server RBAC expression compiles
	if c.ServerRBAC != "" {
		if err := auth.ValidateExpression(c.ServerRBAC); err != nil {
			return fmt.Errorf("invalid server RBAC expression: %w", err)
		}
	}

	// Build driver factory based on configured driver
	var driverName string
	var baseFactory func(namespace string) (orchestra.Driver, error)

	switch {
	case c.HetznerToken != "":
		driverName = "hetzner"
		baseFactory = func(ns string) (orchestra.Driver, error) {
			return hetzner.New(hetzner.Config{
				Namespace:   ns,
				Token:       c.HetznerToken,
				Image:       c.HetznerImage,
				ServerType:  c.HetznerServerType,
				Location:    c.HetznerLocation,
				MaxWorkers:  c.HetznerMaxWorkers,
				ReuseWorker: c.HetznerReuseWorker,
			}, logger)
		}
	case c.DigitalOceanToken != "":
		driverName = "digitalocean"
		baseFactory = func(ns string) (orchestra.Driver, error) {
			return digitalocean.New(digitalocean.Config{
				Namespace:   ns,
				Token:       c.DigitalOceanToken,
				Image:       c.DigitalOceanImage,
				Size:        c.DigitalOceanSize,
				Region:      c.DigitalOceanRegion,
				MaxWorkers:  c.DigitalOceanMaxWorkers,
				ReuseWorker: c.DigitalOceanReuseWorker,
			}, logger)
		}
	case c.FlyToken != "":
		driverName = "fly"
		baseFactory = func(ns string) (orchestra.Driver, error) {
			return fly.New(fly.Config{
				Namespace: ns,
				Token:     c.FlyToken,
				App:       c.FlyApp,
				Region:    c.FlyRegion,
				Org:       c.FlyOrg,
				Size:      c.FlySize,
			}, logger)
		}
	case c.K8sKubeconfig != "" || k8s.IsAvailable():
		driverName = "k8s"
		baseFactory = func(ns string) (orchestra.Driver, error) {
			return k8s.New(k8s.Config{
				Namespace:    ns,
				Kubeconfig:   c.K8sKubeconfig,
				K8sNamespace: c.K8sNamespace,
			}, logger)
		}
	case c.QEMUImage != "":
		driverName = "qemu"
		baseFactory = func(ns string) (orchestra.Driver, error) {
			return qemu.New(qemu.Config{
				Namespace: ns,
				Memory:    c.QEMUMemory,
				CPUs:      c.QEMUCPUs,
				Accel:     c.QEMUAccel,
				Binary:    c.QEMUBinary,
				CacheDir:  c.QEMUCacheDir,
				Image:     c.QEMUImage,
			}, logger)
		}
	default: // docker (including when DockerHost is empty = local socket)
		driverName = "docker"
		baseFactory = func(ns string) (orchestra.Driver, error) {
			return docker.New(docker.Config{
				Namespace: ns,
				Host:      c.DockerHost,
			}, logger)
		}
	}

	// Optionally wrap with native fallback or cache
	driverFactory := baseFactory
	if c.CacheURL != "" {
		params := map[string]string{"cache": c.CacheURL}
		if c.CacheCompression != "" {
			params["cache_compression"] = c.CacheCompression
		}
		if c.CachePrefix != "" {
			params["cache_prefix"] = c.CachePrefix
		}
		driverFactory = func(ns string) (orchestra.Driver, error) {
			d, err := baseFactory(ns)
			if err != nil {
				return nil, err
			}
			return cache.WrapWithCaching(d, params, logger)
		}
	}

	router, err := server.NewRouter(logger, client, server.RouterOptions{
		MaxInFlight:           c.MaxInFlight,
		WebhookTimeout:        c.WebhookTimeout,
		BasicAuthUsername:     basicAuthUsername,
		BasicAuthPassword:     basicAuthPassword,
		AllowedDrivers:        c.AllowedDrivers,
		AllowedFeatures:       c.AllowedFeatures,
		SecretsManager:        secretsManager,
		FetchTimeout:          c.FetchTimeout,
		FetchMaxResponseBytes: int64(c.FetchMaxResponseMB) * 1024 * 1024,
		AuthConfig:            authConfig,
		ObservabilityProvider: obsProvider,
		DriverFactory:         driverFactory,
		DriverName:            driverName,
	})
	if err != nil {
		return fmt.Errorf("could not create router: %w", err)
	}

	router.ProtectedGroup().GET("/tasks/*", func(ctx *echo.Context) error {
		lookupPath := ctx.Param("*")
		if lookupPath == "" || lookupPath[0] != '/' {
			lookupPath = "/" + lookupPath
		}

		results, err := client.GetAll(ctx.Request().Context(), lookupPath, []string{"status"})
		if err != nil {
			return fmt.Errorf("could not get all results: %w", err)
		}

		return ctx.Render(http.StatusOK, "results.html", map[string]any{
			"Tree": results.AsTree(),
			"Path": lookupPath,
		})
	})

	router.ProtectedGroup().GET("/graph/*", func(ctx *echo.Context) error {
		lookupPath := ctx.Param("*")
		if lookupPath == "" || lookupPath[0] != '/' {
			lookupPath = "/" + lookupPath
		}

		results, err := client.GetAll(ctx.Request().Context(), lookupPath, []string{"status", "dependsOn"})
		if err != nil {
			return fmt.Errorf("could not get all results: %w", err)
		}

		tree := results.AsTree()
		treeJSON, err := json.Marshal(tree)
		if err != nil {
			return fmt.Errorf("could not marshal tree: %w", err)
		}

		return ctx.Render(http.StatusOK, "graph.html", map[string]any{
			"Tree":     tree,
			"TreeJSON": string(treeJSON),
			"Path":     lookupPath,
		})
	})

	err = router.Start(fmt.Sprintf(":%d", c.Port))
	if err != nil {
		return fmt.Errorf("could not start server: %w", err)
	}

	return nil
}
