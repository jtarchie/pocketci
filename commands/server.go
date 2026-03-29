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

	"github.com/jtarchie/pocketci/cache"
	cachepluginfs "github.com/jtarchie/pocketci/cache/filesystem"
	cacheplugins3 "github.com/jtarchie/pocketci/cache/s3"
	"github.com/jtarchie/pocketci/observability"
	"github.com/jtarchie/pocketci/observability/honeybadger"
	"github.com/jtarchie/pocketci/observability/posthog"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/digitalocean"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/fly"
	"github.com/jtarchie/pocketci/orchestra/hetzner"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/orchestra/qemu"
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
	Port int `default:"8080" env:"CI_PORT" help:"Port to run the server on"`
	// Storage backend: SQLite (default) or S3
	StorageSQLitePath        string        `default:"test.db"                     env:"CI_STORAGE_SQLITE_PATH"                                           help:"SQLite storage database file path (use ':memory:' for in-memory)"                                       name:"storage-sqlite-path"`
	StorageS3Bucket          string        `env:"CI_STORAGE_S3_BUCKET"            help:"S3 bucket name for storage backend"                              name:"storage-s3-bucket"`
	StorageS3Endpoint        string        `env:"CI_STORAGE_S3_ENDPOINT"          help:"S3-compatible endpoint URL"                                      name:"storage-s3-endpoint"`
	StorageS3Region          string        `env:"CI_STORAGE_S3_REGION"            help:"AWS region for S3 storage backend"                               name:"storage-s3-region"`
	StorageS3AccessKeyID     string        `env:"CI_STORAGE_S3_ACCESS_KEY_ID"     help:"S3 access key ID for storage backend"                            name:"storage-s3-access-key-id"`
	StorageS3SecretAccessKey string        `env:"CI_STORAGE_S3_SECRET_ACCESS_KEY" help:"S3 secret access key for storage backend"                        name:"storage-s3-secret-access-key"`
	StorageS3Prefix          string        `env:"CI_STORAGE_S3_PREFIX"            help:"S3 key prefix for storage backend"                               name:"storage-s3-prefix"`
	MaxInFlight              int           `default:"10"                          env:"CI_MAX_IN_FLIGHT"                                                 help:"Maximum concurrent pipeline executions"`
	MaxQueueSize             int           `default:"100"                         env:"CI_MAX_QUEUE_SIZE"                                                help:"Maximum queued pipeline executions (0 disables queuing)"`
	WebhookTimeout           time.Duration `default:"5s"                          env:"CI_WEBHOOK_TIMEOUT"                                               help:"Timeout waiting for pipeline webhook response"`
	DedupTTL                 time.Duration `default:"168h"                        env:"CI_DEDUP_TTL"                                                     help:"TTL for webhook dedup entries (default 7 days)"`
	BasicAuth                string        `env:"CI_BASIC_AUTH"                   help:"Basic auth credentials in format 'username:password' (optional)"`
	AllowedDrivers           string        `default:"*"                           env:"CI_ALLOWED_DRIVERS"                                               help:"Comma-separated list of allowed driver names (e.g., 'docker,native,k8s'), or '*' for all"`
	AllowedFeatures          string        `default:"*"                           env:"CI_ALLOWED_FEATURES"                                              help:"Comma-separated list of allowed features (webhooks,secrets,notifications,fetch,resume), or '*' for all"`
	FetchTimeout             time.Duration `default:"30s"                         env:"CI_FETCH_TIMEOUT"                                                 help:"Default timeout for fetch() calls in pipelines"`
	FetchMaxResponseMB       int           `default:"10"                          env:"CI_FETCH_MAX_RESPONSE_MB"                                         help:"Maximum response body size in MB for fetch() calls"                                                     name:"fetch-max-response-mb"`
	// SQLite secrets backend
	SecretsSQLitePath       string `default:"test.db"                  env:"CI_SECRETS_SQLITE_PATH"                                       help:"SQLite secrets database file path (use ':memory:' for in-memory)" name:"secrets-sqlite-path"`
	SecretsSQLitePassphrase string `env:"CI_SECRETS_SQLITE_PASSPHRASE" help:"Encryption passphrase for SQLite secrets backend (required)" name:"secrets-sqlite-passphrase"`
	// S3 secrets backend (takes precedence over SQLite when Bucket is set)
	SecretsS3Bucket          string   `env:"CI_SECRETS_S3_BUCKET"                                help:"S3 bucket name for secrets backend"                                           name:"secrets-s3-bucket"`
	SecretsS3Endpoint        string   `env:"CI_SECRETS_S3_ENDPOINT"                              help:"S3-compatible endpoint URL (e.g., 'https://s3.amazonaws.com')"                name:"secrets-s3-endpoint"`
	SecretsS3Region          string   `env:"CI_SECRETS_S3_REGION"                                help:"AWS region for S3 secrets backend"                                            name:"secrets-s3-region"`
	SecretsS3AccessKeyID     string   `env:"CI_SECRETS_S3_ACCESS_KEY_ID"                         help:"S3 access key ID"                                                             name:"secrets-s3-access-key-id"`
	SecretsS3SecretAccessKey string   `env:"CI_SECRETS_S3_SECRET_ACCESS_KEY"                     help:"S3 secret access key"                                                         name:"secrets-s3-secret-access-key"`
	SecretsS3Passphrase      string   `env:"CI_SECRETS_S3_PASSPHRASE"                            help:"Encryption passphrase for S3 secrets backend (application-layer AES-256-GCM)" name:"secrets-s3-passphrase"`
	SecretsS3Encrypt         string   `env:"CI_SECRETS_S3_ENCRYPT"                               help:"S3 server-side encryption mode: sse-s3, sse-kms, or sse-c"                    name:"secrets-s3-encrypt"`
	SecretsS3Prefix          string   `env:"CI_SECRETS_S3_PREFIX"                                help:"S3 key prefix for secrets"                                                    name:"secrets-s3-prefix"`
	Secret                   []string `help:"Set a global secret as KEY=VALUE (can be repeated)" short:"e"`
	PosthogAPIKey            string   `env:"CI_POSTHOG_API_KEY"                                  help:"PostHog API key (e.g., 'phc_abc123')"                                         name:"posthog-api-key"`
	PosthogEndpoint          string   `env:"CI_POSTHOG_ENDPOINT"                                 help:"PostHog ingestion endpoint URL (defaults to PostHog cloud)"`
	HoneybadgerAPIKey        string   `env:"CI_HONEYBADGER_API_KEY"                              help:"Honeybadger API key"                                                          name:"honeybadger-api-key"`
	HoneybadgerEnv           string   `env:"CI_HONEYBADGER_ENV"                                  help:"Honeybadger environment name (e.g., 'production')"`

	// OAuth provider configuration
	OAuthGithubClientID        string `env:"CI_OAUTH_GITHUB_CLIENT_ID"        help:"GitHub OAuth application client ID"                            name:"oauth-github-client-id"`
	OAuthGithubClientSecret    string `env:"CI_OAUTH_GITHUB_CLIENT_SECRET"    help:"GitHub OAuth application client secret"                        name:"oauth-github-client-secret"`
	OAuthGitlabClientID        string `env:"CI_OAUTH_GITLAB_CLIENT_ID"        help:"GitLab OAuth application client ID"                            name:"oauth-gitlab-client-id"`
	OAuthGitlabClientSecret    string `env:"CI_OAUTH_GITLAB_CLIENT_SECRET"    help:"GitLab OAuth application client secret"                        name:"oauth-gitlab-client-secret"`
	OAuthGitlabURL             string `env:"CI_OAUTH_GITLAB_URL"              help:"Self-hosted GitLab URL (defaults to https://gitlab.com)"       name:"oauth-gitlab-url"`
	OAuthMicrosoftClientID     string `env:"CI_OAUTH_MICROSOFT_CLIENT_ID"     help:"Microsoft/Azure AD OAuth client ID"                            name:"oauth-microsoft-client-id"`
	OAuthMicrosoftClientSecret string `env:"CI_OAUTH_MICROSOFT_CLIENT_SECRET" help:"Microsoft/Azure AD OAuth client secret"                        name:"oauth-microsoft-client-secret"`
	OAuthMicrosoftTenant       string `env:"CI_OAUTH_MICROSOFT_TENANT"        help:"Azure AD tenant ID (defaults to 'common')"                     name:"oauth-microsoft-tenant"`
	OAuthSessionSecret         string `env:"CI_OAUTH_SESSION_SECRET"          help:"Secret key for encrypting OAuth session cookies"               name:"oauth-session-secret"`
	OAuthCallbackURL           string `env:"CI_OAUTH_CALLBACK_URL"            help:"Base URL for OAuth callbacks (e.g., 'https://ci.example.com')" name:"oauth-callback-url"`

	// RBAC configuration
	ServerRBAC string `env:"CI_SERVER_RBAC" help:"Expr expression for server-level access control (e.g., 'Email endsWith \"@company.com\"')" name:"server-rbac"`

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
	K8sKubeconfig string `env:"CI_K8S_KUBECONFIG" help:"Path to kubeconfig file (uses in-cluster config if empty)" name:"k8s-kubeconfig"`
	K8sNamespace  string `env:"CI_K8S_NAMESPACE"  help:"Kubernetes namespace for jobs (default: default)"          name:"k8s-namespace"`

	// QEMU driver
	QEMUMemory   string `env:"CI_QEMU_MEMORY"    help:"QEMU VM memory (e.g., '2048')"             name:"qemu-memory"`
	QEMUCPUs     string `env:"CI_QEMU_CPUS"      help:"QEMU VM CPU count"                         name:"qemu-cpus"`
	QEMUAccel    string `env:"CI_QEMU_ACCEL"     help:"QEMU acceleration: hvf, kvm, tcg, or auto" name:"qemu-accel"`
	QEMUImage    string `env:"CI_QEMU_IMAGE"     help:"QEMU boot image path or URL"               name:"qemu-image"`
	QEMUBinary   string `env:"CI_QEMU_BINARY"    help:"Path to qemu-system binary"                name:"qemu-binary"`
	QEMUCacheDir string `env:"CI_QEMU_CACHE_DIR" help:"Directory for QEMU image cache"            name:"qemu-cache-dir"`

	// Cache (optional, wraps the driver)
	CacheS3Bucket          string        `env:"CI_CACHE_S3_BUCKET"            help:"S3 bucket for cache backend"                            name:"cache-s3-bucket"`
	CacheS3Prefix          string        `env:"CI_CACHE_S3_PREFIX"            help:"S3 key prefix for cache"                                name:"cache-s3-prefix"`
	CacheS3Endpoint        string        `env:"CI_CACHE_S3_ENDPOINT"          help:"S3-compatible endpoint URL for cache"                   name:"cache-s3-endpoint"`
	CacheS3Region          string        `env:"CI_CACHE_S3_REGION"            help:"AWS region for cache S3 backend"                        name:"cache-s3-region"`
	CacheS3AccessKeyID     string        `env:"CI_CACHE_S3_ACCESS_KEY_ID"     help:"S3 access key ID for cache"                             name:"cache-s3-access-key-id"`
	CacheS3SecretAccessKey string        `env:"CI_CACHE_S3_SECRET_ACCESS_KEY" help:"S3 secret access key for cache"                         name:"cache-s3-secret-access-key"`
	CacheS3TTL             time.Duration `env:"CI_CACHE_S3_TTL"               help:"Cache object TTL (0 = no expiry)"                       name:"cache-s3-ttl"`
	CacheS3PartSize        int64         `env:"CI_CACHE_S3_PART_SIZE"         help:"S3 multipart upload part size in bytes (default: 10MB)" name:"cache-s3-part-size"`
	CacheS3Concurrency     int           `env:"CI_CACHE_S3_CONCURRENCY"       help:"S3 multipart upload concurrency (default: 3)"           name:"cache-s3-concurrency"`
	CacheFilesystemDir     string        `env:"CI_CACHE_FILESYSTEM_DIR"       help:"Directory for filesystem cache backend"                 name:"cache-filesystem-dir"`
	CacheFilesystemTTL     time.Duration `env:"CI_CACHE_FILESYSTEM_TTL"       help:"Filesystem cache TTL (0 = no expiry)"                   name:"cache-filesystem-ttl"`
	CacheCompression       string        `env:"CI_CACHE_COMPRESSION"          help:"Cache compression: zstd, gzip, or none (default: zstd)"`
	CacheKeyPrefix         string        `env:"CI_CACHE_KEY_PREFIX"           help:"Cache key prefix"`
	// Profiling
	PprofAddr string `env:"CI_PPROF_ADDR" help:"Address to serve pprof debug endpoints (e.g. ':6060'). Empty disables profiling." name:"pprof-addr"`
}

func (c *Server) Run(logger *slog.Logger) error {
	obsProvider, logger, err := c.initObservability(logger)
	if err != nil {
		return err
	}

	if obsProvider != nil {
		defer func() { _ = obsProvider.Close() }()
	}

	client, err := c.initStorage(logger)
	if err != nil {
		return err
	}

	defer func() { _ = client.Close() }()

	secretsManager, err := c.initSecrets(logger)
	if err != nil {
		return err
	}

	defer func() { _ = secretsManager.Close() }()

	if err := c.storeGlobalSecrets(secretsManager); err != nil {
		return err
	}

	authConfig, basicAuthUsername, basicAuthPassword, err := c.buildAuthConfig()
	if err != nil {
		return err
	}

	driverConfigs := c.buildDriverConfigs()
	defaultDriver := c.defaultDriverName()

	cacheStore, err := c.initCacheStore()
	if err != nil {
		return err
	}

	router, err := server.NewRouter(logger, client, server.RouterOptions{
		MaxInFlight:           c.MaxInFlight,
		MaxQueueSize:          c.MaxQueueSize,
		WebhookTimeout:        c.WebhookTimeout,
		BasicAuthUsername:     basicAuthUsername,
		BasicAuthPassword:     basicAuthPassword,
		AllowedDrivers:        c.AllowedDrivers,
		AllowedFeatures:       c.AllowedFeatures,
		SecretsManager:        secretsManager,
		FetchTimeout:          c.FetchTimeout,
		FetchMaxResponseBytes: int64(c.FetchMaxResponseMB) * 1024 * 1024,
		DedupTTL:              c.DedupTTL,
		AuthConfig:            authConfig,
		ObservabilityProvider: obsProvider,
		DefaultDriver:         defaultDriver,
		DriverConfigs:         driverConfigs,
		CacheStore:            cacheStore,
		CacheCompression:      c.CacheCompression,
		CacheKeyPrefix:        c.CacheKeyPrefix,
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

	c.registerRoutes(router, client)

	if c.PprofAddr != "" {
		server.StartPprof(c.PprofAddr, logger)
	}

	err = router.Start(fmt.Sprintf(":%d", c.Port))
	if err != nil {
		return fmt.Errorf("could not start server: %w", err)
	}

	return nil
}

func (c *Server) initObservability(logger *slog.Logger) (observability.Provider, *slog.Logger, error) {
	var obsProvider observability.Provider

	switch {
	case c.PosthogAPIKey != "":
		var err error

		obsProvider, err = posthog.New(posthog.Config{
			APIKey:   c.PosthogAPIKey,
			Endpoint: c.PosthogEndpoint,
		}, logger)
		if err != nil {
			return nil, logger, fmt.Errorf("could not create posthog provider: %w", err)
		}

	case c.HoneybadgerAPIKey != "":
		var err error

		obsProvider, err = honeybadger.New(honeybadger.Config{
			APIKey: c.HoneybadgerAPIKey,
			Env:    c.HoneybadgerEnv,
		}, logger)
		if err != nil {
			return nil, logger, fmt.Errorf("could not create honeybadger provider: %w", err)
		}
	}

	if obsProvider != nil {
		logger = slog.New(obsProvider.SlogHandler(logger.Handler()))
		slog.SetDefault(logger)
	}

	return obsProvider, logger, nil
}

func (c *Server) initStorage(logger *slog.Logger) (storage.Driver, error) {
	switch {
	case c.StorageS3Bucket != "":
		client, err := storages3.NewS3(storages3.Config{
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
			return nil, fmt.Errorf("could not create S3 storage client: %w", err)
		}

		return client, nil
	default:
		client, err := storagesqlite.NewSqlite(storagesqlite.Config{
			Path: c.StorageSQLitePath,
		}, "", logger)
		if err != nil {
			return nil, fmt.Errorf("could not create SQLite storage client: %w", err)
		}

		return client, nil
	}
}

func (c *Server) initSecrets(logger *slog.Logger) (secrets.Manager, error) {
	switch {
	case c.SecretsS3Bucket != "":
		mgr, err := secretss3.New(secretss3.Config{
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
			return nil, fmt.Errorf("could not create S3 secrets manager: %w", err)
		}

		return mgr, nil
	default:
		mgr, err := secretssqlite.New(secretssqlite.Config{
			Path:       c.SecretsSQLitePath,
			Passphrase: c.SecretsSQLitePassphrase,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("could not create SQLite secrets manager: %w", err)
		}

		return mgr, nil
	}
}

func (c *Server) storeGlobalSecrets(mgr secrets.Manager) error {
	for _, s := range c.Secret {
		key, value, found := strings.Cut(s, "=")
		if !found || key == "" {
			return fmt.Errorf("invalid --secret flag %q: expected KEY=VALUE format", s)
		}

		if err := mgr.Set(context.Background(), secrets.GlobalScope, key, value); err != nil {
			return fmt.Errorf("could not set global secret %q: %w", key, err)
		}
	}

	return nil
}

func (c *Server) buildAuthConfig() (*auth.Config, string, string, error) {
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

	var basicAuthUsername, basicAuthPassword string
	if c.BasicAuth != "" {
		parts := strings.SplitN(c.BasicAuth, ":", 2)
		if len(parts) != 2 {
			return nil, "", "", fmt.Errorf("invalid basic auth format: expected 'username:password', got '%s'", c.BasicAuth)
		}

		basicAuthUsername = parts[0]
		basicAuthPassword = parts[1]

		if basicAuthUsername == "" || basicAuthPassword == "" {
			return nil, "", "", errors.New("basic auth username and password cannot be empty")
		}
	}

	if c.BasicAuth != "" && authConfig.HasOAuthProviders() {
		return nil, "", "", errors.New("basic auth and OAuth providers cannot be used together: choose one authentication method")
	}

	if authConfig.HasOAuthProviders() && authConfig.SessionSecret == "" {
		return nil, "", "", errors.New("CI_OAUTH_SESSION_SECRET is required when OAuth providers are configured")
	}

	if authConfig.HasOAuthProviders() && authConfig.CallbackURL == "" {
		return nil, "", "", errors.New("CI_OAUTH_CALLBACK_URL is required when OAuth providers are configured")
	}

	if c.ServerRBAC != "" {
		if err := auth.ValidateExpression(c.ServerRBAC); err != nil {
			return nil, "", "", fmt.Errorf("invalid server RBAC expression: %w", err)
		}
	}

	return authConfig, basicAuthUsername, basicAuthPassword, nil
}

func (c *Server) buildDriverConfigs() map[string]orchestra.DriverConfig {
	driverConfigs := map[string]orchestra.DriverConfig{}

	driverConfigs["docker"] = docker.ServerConfig{Host: c.DockerHost}
	driverConfigs["native"] = native.ServerConfig{}

	if c.HetznerToken != "" {
		driverConfigs["hetzner"] = hetzner.ServerConfig{
			Token:       c.HetznerToken,
			Image:       c.HetznerImage,
			ServerType:  c.HetznerServerType,
			Location:    c.HetznerLocation,
			MaxWorkers:  c.HetznerMaxWorkers,
			ReuseWorker: c.HetznerReuseWorker,
		}
	}

	if c.DigitalOceanToken != "" {
		driverConfigs["digitalocean"] = digitalocean.ServerConfig{
			Token:       c.DigitalOceanToken,
			Image:       c.DigitalOceanImage,
			Size:        c.DigitalOceanSize,
			Region:      c.DigitalOceanRegion,
			MaxWorkers:  c.DigitalOceanMaxWorkers,
			ReuseWorker: c.DigitalOceanReuseWorker,
		}
	}

	if c.FlyToken != "" {
		driverConfigs["fly"] = fly.ServerConfig{
			Token:  c.FlyToken,
			App:    c.FlyApp,
			Region: c.FlyRegion,
			Org:    c.FlyOrg,
			Size:   c.FlySize,
		}
	}

	if c.K8sKubeconfig != "" || k8s.IsAvailable() {
		driverConfigs["k8s"] = k8s.ServerConfig{
			Kubeconfig:   c.K8sKubeconfig,
			K8sNamespace: c.K8sNamespace,
		}
	}

	if c.QEMUImage != "" {
		driverConfigs["qemu"] = qemu.ServerConfig{
			Memory:   c.QEMUMemory,
			CPUs:     c.QEMUCPUs,
			Accel:    c.QEMUAccel,
			Binary:   c.QEMUBinary,
			CacheDir: c.QEMUCacheDir,
			Image:    c.QEMUImage,
		}
	}

	return driverConfigs
}

func (c *Server) defaultDriverName() string {
	switch {
	case c.HetznerToken != "":
		return "hetzner"
	case c.DigitalOceanToken != "":
		return "digitalocean"
	case c.FlyToken != "":
		return "fly"
	case c.K8sKubeconfig != "" || k8s.IsAvailable():
		return "k8s"
	case c.QEMUImage != "":
		return "qemu"
	default:
		return "docker"
	}
}

func (c *Server) initCacheStore() (cache.CacheStore, error) {
	hasS3 := c.CacheS3Bucket != ""
	hasFS := c.CacheFilesystemDir != ""

	if hasS3 && hasFS {
		return nil, errors.New("cannot configure both S3 and filesystem cache backends; choose one")
	}

	if hasFS {
		store, err := cachepluginfs.New(cachepluginfs.Config{
			Directory: c.CacheFilesystemDir,
			TTL:       c.CacheFilesystemTTL,
		})
		if err != nil {
			return nil, fmt.Errorf("could not create filesystem cache store: %w", err)
		}

		return store, nil
	}

	if !hasS3 {
		return nil, nil
	}

	store, err := cacheplugins3.New(context.Background(), cacheplugins3.Config{
		Config: s3config.Config{
			Bucket:          c.CacheS3Bucket,
			Prefix:          c.CacheS3Prefix,
			Endpoint:        c.CacheS3Endpoint,
			Region:          c.CacheS3Region,
			AccessKeyID:     c.CacheS3AccessKeyID,
			SecretAccessKey: c.CacheS3SecretAccessKey,
			ForcePathStyle:  c.CacheS3Endpoint != "",
			TTL:             c.CacheS3TTL,
		},
		PartSize:    c.CacheS3PartSize,
		Concurrency: c.CacheS3Concurrency,
	})
	if err != nil {
		return nil, fmt.Errorf("could not create cache store: %w", err)
	}

	return store, nil
}

func (c *Server) registerRoutes(router *server.Router, client storage.Driver) {
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
}
