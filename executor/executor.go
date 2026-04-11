package executor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/cache"
	cacheplugins3 "github.com/jtarchie/pocketci/cache/s3"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/resources"
	"github.com/jtarchie/pocketci/resources/mock"
	"github.com/jtarchie/pocketci/runtime"
	runtimebackwards "github.com/jtarchie/pocketci/runtime/backwards"
	"github.com/jtarchie/pocketci/s3config"
	"github.com/jtarchie/pocketci/secrets"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
)

type Execute struct {
	StorageSQLitePath       string        `default:"test.db"                                                        env:"CI_STORAGE_SQLITE_PATH"                                  help:"SQLite storage database file path"                                required:""`
	Pipeline                string        `arg:""                                                                   help:"Path to pipeline javascript file"                       type:"existingfile"`
	Driver                  string        `default:"native"                                                         env:"CI_DRIVER"                                               help:"Orchestrator driver name (docker, native, k8s)"`
	DockerHost              string        `env:"CI_DOCKER_HOST"                                                     help:"Docker daemon host URL"`
	K8sKubeconfig           string        `env:"CI_K8S_KUBECONFIG"                                                  help:"Path to kubeconfig file"`
	K8sNamespace            string        `env:"CI_K8S_NAMESPACE"                                                   help:"Kubernetes namespace for jobs"`
	CacheS3Bucket           string        `env:"CI_CACHE_S3_BUCKET"                                                 help:"S3 bucket for cache backend"`
	CacheS3Prefix           string        `env:"CI_CACHE_S3_PREFIX"                                                 help:"S3 key prefix for cache"`
	CacheS3Endpoint         string        `env:"CI_CACHE_S3_ENDPOINT"                                               help:"S3-compatible endpoint URL for cache"`
	CacheS3Region           string        `env:"CI_CACHE_S3_REGION"                                                 help:"AWS region for cache S3 backend"`
	CacheS3AccessKeyID      string        `env:"CI_CACHE_S3_ACCESS_KEY_ID"                                          help:"S3 access key ID for cache"`
	CacheS3SecretAccessKey  string        `env:"CI_CACHE_S3_SECRET_ACCESS_KEY"                                      help:"S3 secret access key for cache"`
	CacheS3TTL              time.Duration `env:"CI_CACHE_S3_TTL"                                                    help:"Cache object TTL (0 = no expiry)"`
	CacheCompression        string        `env:"CI_CACHE_COMPRESSION"                                               help:"Cache compression: zstd, gzip, or none"`
	CacheKeyPrefix          string        `env:"CI_CACHE_KEY_PREFIX"                                                help:"Cache key prefix"`
	Timeout                 time.Duration `env:"CI_TIMEOUT"                                                         help:"timeout for the pipeline, will cause abort if exceeded"`
	Resume                  bool          `help:"Resume from last checkpoint if pipeline was interrupted"`
	RunID                   string        `help:"Unique run ID for resume support (auto-generated if not provided)"`
	SecretsSQLitePath       string        `default:""                                                               env:"CI_SECRETS_SQLITE_PATH"                                  help:"SQLite secrets database file path (use ':memory:' for in-memory)"`
	SecretsSQLitePassphrase string        `default:""                                                               env:"CI_SECRETS_SQLITE_PASSPHRASE"                            help:"Encryption passphrase for SQLite secrets backend"`
	Secret                  []string      `help:"Set a pipeline-scoped secret as KEY=VALUE (can be repeated)"       short:"e"`
	GlobalSecret            []string      `help:"Set a global secret as KEY=VALUE (can be repeated)"`
	FetchTimeout            time.Duration `default:"30s"                                                            env:"CI_FETCH_TIMEOUT"                                        help:"Timeout for fetch() requests in pipelines"`
	FetchMaxResponseMB      int           `default:"10"                                                             env:"CI_FETCH_MAX_RESPONSE_MB"                                help:"Maximum response size in MB for fetch() requests"`
}

var ErrCouldNotBundle = errors.New("could not bundle pipeline")

func youtubeIDStyle(input string) string {
	hash := sha256.Sum256([]byte(input))

	encoded := base64.RawURLEncoding.EncodeToString(hash[:])

	const maxLength = 11

	if len(encoded) > maxLength {
		return encoded[:maxLength] // YouTube IDs are 11 chars
	}

	return encoded
}

func (c *Execute) Run(logger *slog.Logger) error {
	pipelinePath, err := filepath.Abs(c.Pipeline)
	if err != nil {
		return fmt.Errorf("could not get absolute path to pipeline: %w", err)
	}

	runtimeID := youtubeIDStyle(pipelinePath)

	if logger == nil {
		logger = slog.Default()
	}

	logger = logger.WithGroup("runner.run").With(
		"id", runtimeID,
		"pipeline", c.Pipeline,
		"orchestrator", c.Driver,
	)

	// Create a context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if c.Timeout > 0 {
		// Create a context with timeout
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	// Set up signal handling
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Handle signals in a separate goroutine
	go handleSignals(ctx, cancel, logger, sigs)

	// Use a unique namespace per invocation to avoid container name collisions
	// when multiple tests run the same pipeline+driver in parallel.
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	namespace := "ci-" + runtimeID + "-" + hex.EncodeToString(nonce[:])

	driver, err := createDriver(c, namespace, logger)
	if err != nil {
		return err
	}

	defer func() { _ = driver.Close() }()

	driver, err = c.wrapDriverWithCache(driver, logger)
	if err != nil {
		return err
	}

	storage, err := storagesqlite.NewSqlite(storagesqlite.Config{
		Path: c.StorageSQLitePath,
	}, runtimeID, logger)
	if err != nil {
		return fmt.Errorf("could not create sqlite client: %w", err)
	}
	defer func() { _ = storage.Close() }()

	secretsManager, err := initSecretsManager(ctx, c, runtimeID, logger)
	if err != nil {
		return err
	}

	if secretsManager != nil {
		defer func() { _ = secretsManager.Close() }()
	}

	extension := strings.ToLower(filepath.Ext(pipelinePath))
	if extension == ".yml" || extension == ".yaml" {
		return c.runYAMLPipeline(ctx, pipelinePath, runtimeID, driver, storage, secretsManager, logger)
	}

	return c.runJSPipeline(ctx, pipelinePath, runtimeID, driver, storage, secretsManager, logger)
}

func (c *Execute) runJSPipeline(ctx context.Context, pipelinePath, runtimeID string, driver orchestra.Driver, store storage.Driver, secretsManager secrets.Manager, logger *slog.Logger) error {
	pipeline, err := loadPipeline(pipelinePath)
	if err != nil {
		return err
	}

	js := runtime.NewJS(logger)

	opts := runtime.ExecuteOptions{
		Resume:                c.Resume,
		RunID:                 c.RunID,
		PipelineID:            runtimeID,
		SecretsManager:        secretsManager,
		FetchTimeout:          c.FetchTimeout,
		FetchMaxResponseBytes: int64(c.FetchMaxResponseMB) * 1024 * 1024,
		ResourceRegistry:      resources.NewRegistry([]resources.Resource{&mock.Mock{}}),
	}

	if c.Resume && opts.RunID == "" {
		opts.RunID = runtimeID
	}

	err = js.ExecuteWithOptions(ctx, pipeline, driver, store, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("execution cancelled: %w", err)
		}

		return fmt.Errorf("could not execute pipeline: %w", err)
	}

	return nil
}

func (c *Execute) runYAMLPipeline(
	ctx context.Context,
	pipelinePath, runtimeID string,
	driver orchestra.Driver,
	store storage.Driver,
	secretsManager secrets.Manager,
	logger *slog.Logger,
) error {
	cfg, err := backwards.LoadConfig(pipelinePath)
	if err != nil {
		return fmt.Errorf("could not load YAML pipeline: %w", err)
	}

	err = runtimebackwards.ValidateConfig(cfg, nil)
	if err != nil {
		return fmt.Errorf("invalid pipeline: %w", err)
	}

	runID := c.RunID
	if runID == "" {
		runID = runtimeID
	}

	runner := runtimebackwards.New(cfg, driver, store, logger, runID, runtimeID,
		runtimebackwards.RunnerOptions{
			SecretsManager:   secretsManager,
			ResourceRegistry: resources.NewRegistry([]resources.Resource{&mock.Mock{}}),
		},
	)

	err = runner.Run(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("execution cancelled: %w", err)
		}

		return fmt.Errorf("could not execute pipeline: %w", err)
	}

	return nil
}

func parseSecretFlag(s string) (string, string, bool) {
	key, value, found := strings.Cut(s, "=")
	if !found || key == "" {
		return "", "", false
	}

	return key, value, true
}

// loadPipeline bundles a JS/TS pipeline from disk.
func loadPipeline(pipelinePath string) (string, error) {
	result := api.Build(api.BuildOptions{
		EntryPoints:      []string{pipelinePath},
		Bundle:           true,
		Sourcemap:        api.SourceMapInline,
		Platform:         api.PlatformNeutral,
		PreserveSymlinks: true,
		AbsWorkingDir:    filepath.Dir(pipelinePath),
	})
	if len(result.Errors) > 0 {
		return "", fmt.Errorf("%w: %s", ErrCouldNotBundle, result.Errors[0].Text)
	}

	return string(result.OutputFiles[0].Contents), nil
}

// createDriver initializes the appropriate orchestrator driver based on config.
func createDriver(c *Execute, namespace string, logger *slog.Logger) (orchestra.Driver, error) {
	var driver orchestra.Driver
	var err error

	switch c.Driver {
	case "docker":
		driver, err = docker.New(context.Background(), docker.Config{ServerConfig: docker.ServerConfig{Host: c.DockerHost}, Namespace: namespace}, logger)
	case "k8s":
		driver, err = k8s.New(context.Background(), k8s.Config{ServerConfig: k8s.ServerConfig{Kubeconfig: c.K8sKubeconfig, K8sNamespace: c.K8sNamespace}, Namespace: namespace}, logger)
	default: // native
		driver, err = native.New(context.Background(), native.Config{Namespace: namespace}, logger)
	}

	if err != nil {
		return nil, fmt.Errorf("could not create orchestrator client (%q): %w", c.Driver, err)
	}

	return driver, nil
}

// initSecretsManager creates and seeds the secrets manager if configured.
func initSecretsManager(ctx context.Context, c *Execute, runtimeID string, logger *slog.Logger) (secrets.Manager, error) {
	if c.SecretsSQLitePassphrase == "" {
		return nil, nil //nolint:nilnil
	}

	path := c.SecretsSQLitePath
	if path == "" {
		path = ":memory:"
	}

	secretsManager, err := secretssqlite.New(secretssqlite.Config{
		Path:       path,
		Passphrase: c.SecretsSQLitePassphrase,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("could not create secrets manager: %w", err)
	}

	for _, s := range c.Secret {
		key, value, found := parseSecretFlag(s)
		if !found {
			return nil, fmt.Errorf("invalid --secret flag %q: expected KEY=VALUE format", s)
		}

		err := secretsManager.Set(ctx, secrets.PipelineScope(runtimeID), key, value)
		if err != nil {
			return nil, fmt.Errorf("could not set secret %q: %w", key, err)
		}
	}

	for _, s := range c.GlobalSecret {
		key, value, found := parseSecretFlag(s)
		if !found {
			return nil, fmt.Errorf("invalid --global-secret flag %q: expected KEY=VALUE format", s)
		}

		err := secretsManager.Set(ctx, secrets.GlobalScope, key, value)
		if err != nil {
			return nil, fmt.Errorf("could not set global secret %q: %w", key, err)
		}
	}

	return secretsManager, nil
}

func handleSignals(ctx context.Context, cancel context.CancelFunc, logger *slog.Logger, sigs <-chan os.Signal) {
	select {
	case sig, ok := <-sigs:
		if !ok {
			return
		}

		logger.Debug("execution.canceled", "signal", sig)
		cancel()
	case <-ctx.Done():
	}
}

func (c *Execute) wrapDriverWithCache(driver orchestra.Driver, logger *slog.Logger) (orchestra.Driver, error) {
	if c.CacheS3Bucket == "" {
		return driver, nil
	}

	store, err := cacheplugins3.New(context.Background(), cacheplugins3.Config{Config: s3config.Config{
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
		return nil, fmt.Errorf("could not initialize cache layer: %w", err)
	}

	return cache.WrapWithCaching(driver, store, c.CacheCompression, c.CacheKeyPrefix, logger), nil
}
