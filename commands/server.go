package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/cache"
	cacheplugins3 "github.com/jtarchie/pocketci/cache/s3"
	"github.com/jtarchie/pocketci/observability"
	"github.com/jtarchie/pocketci/observability/honeybadger"
	"github.com/jtarchie/pocketci/observability/posthog"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/s3config"
	"github.com/jtarchie/pocketci/secrets"
	secretss3 "github.com/jtarchie/pocketci/secrets/s3"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	storages3 "github.com/jtarchie/pocketci/storage/s3"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
	"github.com/jtarchie/pocketci/webhooks"
	webhookgeneric "github.com/jtarchie/pocketci/webhooks/generic"
	webhookgithub "github.com/jtarchie/pocketci/webhooks/github"
	webhookhoneybadger "github.com/jtarchie/pocketci/webhooks/honeybadger"
	webhookslack "github.com/jtarchie/pocketci/webhooks/slack"
	"github.com/labstack/echo/v5"
)

type Server struct {
	Port int `default:"8080"             env:"CI_PORT"                 help:"Port to run the server on"`
	// Storage backend: SQLite (default) or S3
	StorageSQLitePath        string        `name:"storage-sqlite-path"        default:"test.db" env:"CI_STORAGE_SQLITE_PATH" help:"SQLite storage database file path (use ':memory:' for in-memory)"`
	StorageS3Bucket          string        `name:"storage-s3-bucket"            env:"CI_STORAGE_S3_BUCKET"            help:"S3 bucket name for storage backend"`
	StorageS3Endpoint        string        `name:"storage-s3-endpoint"          env:"CI_STORAGE_S3_ENDPOINT"          help:"S3-compatible endpoint URL"`
	StorageS3Region          string        `name:"storage-s3-region"            env:"CI_STORAGE_S3_REGION"            help:"AWS region for S3 storage backend"`
	StorageS3AccessKeyID     string        `name:"storage-s3-access-key-id"     env:"CI_STORAGE_S3_ACCESS_KEY_ID"     help:"S3 access key ID for storage backend"`
	StorageS3SecretAccessKey string        `name:"storage-s3-secret-access-key" env:"CI_STORAGE_S3_SECRET_ACCESS_KEY" help:"S3 secret access key for storage backend"`
	StorageS3Prefix          string        `name:"storage-s3-prefix"            env:"CI_STORAGE_S3_PREFIX"            help:"S3 key prefix for storage backend"`
	MaxInFlight              int           `default:"10"               env:"CI_MAX_IN_FLIGHT"         help:"Maximum concurrent pipeline executions"`
	WebhookTimeout           time.Duration `default:"5s"               env:"CI_WEBHOOK_TIMEOUT"       help:"Timeout waiting for pipeline webhook response"`
	BasicAuth                string        `env:"CI_BASIC_AUTH"         help:"Basic auth credentials in format 'username:password' (optional)"`
	AllowedDrivers           string        `default:"*"                env:"CI_ALLOWED_DRIVERS"       help:"Comma-separated list of allowed driver names (e.g., 'docker,native,k8s'), or '*' for all"`
	AllowedFeatures          string        `default:"*"                env:"CI_ALLOWED_FEATURES"      help:"Comma-separated list of allowed features (webhooks,secrets,notifications,fetch,resume), or '*' for all"`
	FetchTimeout             time.Duration `default:"30s"              env:"CI_FETCH_TIMEOUT"         help:"Default timeout for fetch() calls in pipelines"`
	FetchMaxResponseMB       int           `name:"fetch-max-response-mb" default:"10"               env:"CI_FETCH_MAX_RESPONSE_MB" help:"Maximum response body size in MB for fetch() calls"`
	// SQLite secrets backend
	SecretsSQLitePath       string `name:"secrets-sqlite-path"       default:"test.db" env:"CI_SECRETS_SQLITE_PATH"       help:"SQLite secrets database file path (use ':memory:' for in-memory)"`
	SecretsSQLitePassphrase string `name:"secrets-sqlite-passphrase" default:"testing"  env:"CI_SECRETS_SQLITE_PASSPHRASE" help:"Encryption passphrase for SQLite secrets backend"`
	// S3 secrets backend (takes precedence over SQLite when Bucket is set)
	SecretsS3Bucket          string   `name:"secrets-s3-bucket"            env:"CI_SECRETS_S3_BUCKET"            help:"S3 bucket name for secrets backend"`
	SecretsS3Endpoint        string   `name:"secrets-s3-endpoint"          env:"CI_SECRETS_S3_ENDPOINT"          help:"S3-compatible endpoint URL (e.g., 'https://s3.amazonaws.com')"`
	SecretsS3Region          string   `name:"secrets-s3-region"            env:"CI_SECRETS_S3_REGION"            help:"AWS region for S3 secrets backend"`
	SecretsS3AccessKeyID     string   `name:"secrets-s3-access-key-id"     env:"CI_SECRETS_S3_ACCESS_KEY_ID"     help:"S3 access key ID"`
	SecretsS3SecretAccessKey string   `name:"secrets-s3-secret-access-key" env:"CI_SECRETS_S3_SECRET_ACCESS_KEY" help:"S3 secret access key"`
	SecretsS3Passphrase      string   `name:"secrets-s3-passphrase"        env:"CI_SECRETS_S3_PASSPHRASE"        help:"Encryption passphrase for S3 secrets backend (application-layer AES-256-GCM)"`
	SecretsS3Encrypt         string   `name:"secrets-s3-encrypt"           env:"CI_SECRETS_S3_ENCRYPT"           help:"S3 server-side encryption mode: sse-s3, sse-kms, or sse-c"`
	SecretsS3Prefix          string   `name:"secrets-s3-prefix"            env:"CI_SECRETS_S3_PREFIX"            help:"S3 key prefix for secrets"`
	Secret                   []string `help:"Set a global secret as KEY=VALUE (can be repeated)" short:"e"`
	PosthogAPIKey            string   `name:"posthog-api-key"     env:"CI_POSTHOG_API_KEY"     help:"PostHog API key (e.g., 'phc_abc123')"`
	PosthogEndpoint          string   `env:"CI_POSTHOG_ENDPOINT"    help:"PostHog ingestion endpoint URL (defaults to PostHog cloud)"`
	HoneybadgerAPIKey        string   `name:"honeybadger-api-key" env:"CI_HONEYBADGER_API_KEY" help:"Honeybadger API key"`
	HoneybadgerEnv           string   `env:"CI_HONEYBADGER_ENV"     help:"Honeybadger environment name (e.g., 'production')"`

	// OAuth provider configuration
	OAuthGithubClientID        string `name:"oauth-github-client-id"        env:"CI_OAUTH_GITHUB_CLIENT_ID"        help:"GitHub OAuth application client ID"`
	OAuthGithubClientSecret    string `name:"oauth-github-client-secret"    env:"CI_OAUTH_GITHUB_CLIENT_SECRET"    help:"GitHub OAuth application client secret"`
	OAuthGitlabClientID        string `name:"oauth-gitlab-client-id"        env:"CI_OAUTH_GITLAB_CLIENT_ID"        help:"GitLab OAuth application client ID"`
	OAuthGitlabClientSecret    string `name:"oauth-gitlab-client-secret"    env:"CI_OAUTH_GITLAB_CLIENT_SECRET"    help:"GitLab OAuth application client secret"`
	OAuthGitlabURL             string `name:"oauth-gitlab-url"              env:"CI_OAUTH_GITLAB_URL"              help:"Self-hosted GitLab URL (defaults to https://gitlab.com)"`
	OAuthMicrosoftClientID     string `name:"oauth-microsoft-client-id"     env:"CI_OAUTH_MICROSOFT_CLIENT_ID"     help:"Microsoft/Azure AD OAuth client ID"`
	OAuthMicrosoftClientSecret string `name:"oauth-microsoft-client-secret" env:"CI_OAUTH_MICROSOFT_CLIENT_SECRET" help:"Microsoft/Azure AD OAuth client secret"`
	OAuthMicrosoftTenant       string `name:"oauth-microsoft-tenant"        env:"CI_OAUTH_MICROSOFT_TENANT"        help:"Azure AD tenant ID (defaults to 'common')"`
	OAuthSessionSecret         string `name:"oauth-session-secret"          env:"CI_OAUTH_SESSION_SECRET"          help:"Secret key for encrypting OAuth session cookies"`
	OAuthCallbackURL           string `name:"oauth-callback-url"            env:"CI_OAUTH_CALLBACK_URL"            help:"Base URL for OAuth callbacks (e.g., 'https://ci.example.com')"`

	// RBAC configuration
	ServerRBAC string `name:"server-rbac" env:"CI_SERVER_RBAC" help:"Expr expression for server-level access control (e.g., 'Email endsWith \"@company.com\"')"`

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
	K8sKubeconfig string `name:"k8s-kubeconfig" env:"CI_K8S_KUBECONFIG" help:"Path to kubeconfig file (uses in-cluster config if empty)"`
	K8sNamespace  string `name:"k8s-namespace"  env:"CI_K8S_NAMESPACE"  help:"Kubernetes namespace for jobs (default: default)"`

	// QEMU driver
	QEMUMemory   string `name:"qemu-memory"    env:"CI_QEMU_MEMORY"    help:"QEMU VM memory (e.g., '2048')"`
	QEMUCPUs     string `name:"qemu-cpus"      env:"CI_QEMU_CPUS"      help:"QEMU VM CPU count"`
	QEMUAccel    string `name:"qemu-accel"     env:"CI_QEMU_ACCEL"     help:"QEMU acceleration: hvf, kvm, tcg, or auto"`
	QEMUImage    string `name:"qemu-image"     env:"CI_QEMU_IMAGE"     help:"QEMU boot image path or URL"`
	QEMUBinary   string `name:"qemu-binary"    env:"CI_QEMU_BINARY"    help:"Path to qemu-system binary"`
	QEMUCacheDir string `name:"qemu-cache-dir" env:"CI_QEMU_CACHE_DIR" help:"Directory for QEMU image cache"`

	// Cache (optional, wraps the driver)
	CacheS3Bucket          string        `name:"cache-s3-bucket"            env:"CI_CACHE_S3_BUCKET"            help:"S3 bucket for cache backend"`
	CacheS3Prefix          string        `name:"cache-s3-prefix"            env:"CI_CACHE_S3_PREFIX"            help:"S3 key prefix for cache"`
	CacheS3Endpoint        string        `name:"cache-s3-endpoint"          env:"CI_CACHE_S3_ENDPOINT"          help:"S3-compatible endpoint URL for cache"`
	CacheS3Region          string        `name:"cache-s3-region"            env:"CI_CACHE_S3_REGION"            help:"AWS region for cache S3 backend"`
	CacheS3AccessKeyID     string        `name:"cache-s3-access-key-id"     env:"CI_CACHE_S3_ACCESS_KEY_ID"     help:"S3 access key ID for cache"`
	CacheS3SecretAccessKey string        `name:"cache-s3-secret-access-key" env:"CI_CACHE_S3_SECRET_ACCESS_KEY" help:"S3 secret access key for cache"`
	CacheS3TTL             time.Duration `name:"cache-s3-ttl"               env:"CI_CACHE_S3_TTL"               help:"Cache object TTL (0 = no expiry)"`
	CacheCompression       string        `env:"CI_CACHE_COMPRESSION"          help:"Cache compression: zstd, gzip, or none (default: zstd)"`
	CacheKeyPrefix         string        `env:"CI_CACHE_KEY_PREFIX"           help:"Cache key prefix"`
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

	var client storage.Driver
	var err error

	switch {
	case c.StorageS3Bucket != "":
		client, err = storages3.NewS3(storages3.Config{
			Config: s3config.Config{
				Bucket:          c.StorageS3Bucket,
				Prefix:          c.StorageS3Prefix,
				Endpoint:        c.StorageS3Endpoint,
				Region:          c.StorageS3Region,
				AccessKeyID:     c.StorageS3AccessKeyID,
				SecretAccessKey: c.StorageS3SecretAccessKey,
				ForcePathStyle:  c.StorageS3Endpoint != "",
			},
		}, "", logger)
		if err != nil {
			return fmt.Errorf("could not create S3 storage client: %w", err)
		}
	default:
		client, err = storagesqlite.NewSqlite(storagesqlite.Config{
			Path: c.StorageSQLitePath,
		}, "", logger)
		if err != nil {
			return fmt.Errorf("could not create SQLite storage client: %w", err)
		}
	}

	defer func() { _ = client.Close() }()

	// Initialize secrets manager
	var secretsManager secrets.Manager

	switch {
	case c.SecretsS3Bucket != "":
		secretsManager, err = secretss3.New(secretss3.Config{
			Config: s3config.Config{
				Bucket:          c.SecretsS3Bucket,
				Prefix:          c.SecretsS3Prefix,
				Endpoint:        c.SecretsS3Endpoint,
				Region:          c.SecretsS3Region,
				AccessKeyID:     c.SecretsS3AccessKeyID,
				SecretAccessKey: c.SecretsS3SecretAccessKey,
				Key:             c.SecretsS3Passphrase,
				EncryptMode:     c.SecretsS3Encrypt,
				ForcePathStyle:  c.SecretsS3Endpoint != "",
			},
		}, logger)
		if err != nil {
			return fmt.Errorf("could not create S3 secrets manager: %w", err)
		}
		defer func() { _ = secretsManager.Close() }()
	default:
		secretsManager, err = secretssqlite.New(secretssqlite.Config{
			Path:       c.SecretsSQLitePath,
			Passphrase: c.SecretsSQLitePassphrase,
		}, logger)
		if err != nil {
			return fmt.Errorf("could not create SQLite secrets manager: %w", err)
		}
		defer func() { _ = secretsManager.Close() }()
	}

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

	// Build driver config map and determine default driver name.
	var driverName string
	driverConfig := map[string]string{}

	switch {
	case c.HetznerToken != "":
		driverName = "hetzner"
		driverConfig["token"] = c.HetznerToken
		driverConfig["image"] = c.HetznerImage
		driverConfig["server_type"] = c.HetznerServerType
		driverConfig["location"] = c.HetznerLocation
		if c.HetznerMaxWorkers > 0 {
			driverConfig["max_workers"] = fmt.Sprintf("%d", c.HetznerMaxWorkers)
		}
		if c.HetznerReuseWorker {
			driverConfig["reuse_worker"] = "true"
		}
	case c.DigitalOceanToken != "":
		driverName = "digitalocean"
		driverConfig["token"] = c.DigitalOceanToken
		driverConfig["image"] = c.DigitalOceanImage
		driverConfig["size"] = c.DigitalOceanSize
		driverConfig["region"] = c.DigitalOceanRegion
		if c.DigitalOceanMaxWorkers > 0 {
			driverConfig["max_workers"] = fmt.Sprintf("%d", c.DigitalOceanMaxWorkers)
		}
		if c.DigitalOceanReuseWorker {
			driverConfig["reuse_worker"] = "true"
		}
	case c.FlyToken != "":
		driverName = "fly"
		driverConfig["token"] = c.FlyToken
		driverConfig["app"] = c.FlyApp
		driverConfig["region"] = c.FlyRegion
		driverConfig["org"] = c.FlyOrg
		driverConfig["size"] = c.FlySize
	case c.K8sKubeconfig != "" || k8s.IsAvailable():
		driverName = "k8s"
		driverConfig["kubeconfig"] = c.K8sKubeconfig
		driverConfig["namespace"] = c.K8sNamespace
	case c.QEMUImage != "":
		driverName = "qemu"
		driverConfig["memory"] = c.QEMUMemory
		driverConfig["cpus"] = c.QEMUCPUs
		driverConfig["accel"] = c.QEMUAccel
		driverConfig["binary"] = c.QEMUBinary
		driverConfig["cache_dir"] = c.QEMUCacheDir
		driverConfig["image"] = c.QEMUImage
	default: // docker (including when DockerHost is empty = local socket)
		driverName = "docker"
		driverConfig["host"] = c.DockerHost
	}

	// Build driver factory via registry
	baseFactory := func(ns string) (orchestra.Driver, error) {
		return orchestra.CreateDriver(driverName, ns, driverConfig, logger)
	}

	// Optionally wrap with cache
	driverFactory := baseFactory
	if c.CacheS3Bucket != "" {
		store, err := cacheplugins3.New(cacheplugins3.Config{Config: s3config.Config{
			Bucket:          c.CacheS3Bucket,
			Prefix:          c.CacheS3Prefix,
			Endpoint:        c.CacheS3Endpoint,
			Region:          c.CacheS3Region,
			AccessKeyID:     c.CacheS3AccessKeyID,
			SecretAccessKey: c.CacheS3SecretAccessKey,
			ForcePathStyle:  c.CacheS3Endpoint != "",
			TTL:             c.CacheS3TTL,
		}})
		if err != nil {
			return fmt.Errorf("could not create cache store: %w", err)
		}
		driverFactory = func(ns string) (orchestra.Driver, error) {
			d, err := baseFactory(ns)
			if err != nil {
				return nil, err
			}
			return cache.WrapWithCaching(d, store, c.CacheCompression, c.CacheKeyPrefix, logger), nil
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
		DefaultDriverConfig:   driverConfig,
		WebhookProviders: []webhooks.Provider{
			webhookgithub.New(),
			webhookhoneybadger.New(),
			webhookslack.New(),
			webhookgeneric.New(),
		},
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
