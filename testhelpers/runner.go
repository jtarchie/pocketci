package testhelpers

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
	"strings"
	"syscall"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/cache"
	"github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/orchestra/k8s"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/runtime"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/storage"
)

type Runner struct {
	Storage            string        `default:"sqlite://test.db"                                    env:"CI_STORAGE"              help:"Path to storage file"                                                                                                                                      required:""`
	Pipeline           string        `arg:""                                                        help:"Path to pipeline javascript file"                                                                                                                          type:"existingfile"`
	Driver             string        `default:"native"                                              env:"CI_DRIVER"               help:"Orchestrator driver name (docker, native, k8s)"`
	DockerHost         string        `env:"CI_DOCKER_HOST"         help:"Docker daemon host URL"`
	K8sKubeconfig      string        `env:"CI_K8S_KUBECONFIG"      help:"Path to kubeconfig file"`
	K8sNamespace       string        `env:"CI_K8S_NAMESPACE"       help:"Kubernetes namespace for jobs"`
	CacheURL           string        `env:"CI_CACHE_URL"           help:"Cache store URL"`
	CacheCompression   string        `env:"CI_CACHE_COMPRESSION"   help:"Cache compression: zstd, gzip, or none"`
	CachePrefix        string        `env:"CI_CACHE_PREFIX"        help:"Cache key prefix"`
	Timeout            time.Duration `env:"CI_TIMEOUT"                                              help:"timeout for the pipeline, will cause abort if exceeded"`
	Resume             bool          `help:"Resume from last checkpoint if pipeline was interrupted"`
	RunID              string        `help:"Unique run ID for resume support (auto-generated if not provided)"`
	Secrets            string        `default:"" env:"CI_SECRETS" help:"Secrets backend DSN (e.g., 'sqlite://secrets.db?key=my-passphrase)')" `
	Secret             []string      `help:"Set a pipeline-scoped secret as KEY=VALUE (can be repeated)" short:"e"`
	GlobalSecret       []string      `help:"Set a global secret as KEY=VALUE (can be repeated)"`
	FetchTimeout       time.Duration `default:"30s"                                              env:"CI_FETCH_TIMEOUT"            help:"Timeout for fetch() requests in pipelines"`
	FetchMaxResponseMB int           `default:"10"                                               env:"CI_FETCH_MAX_RESPONSE_MB"    help:"Maximum response size in MB for fetch() requests"`
}

func youtubeIDStyle(input string) string {
	hash := sha256.Sum256([]byte(input))

	encoded := base64.RawURLEncoding.EncodeToString(hash[:])

	const maxLength = 11

	if len(encoded) > maxLength {
		return encoded[:maxLength] // YouTube IDs are 11 chars
	}

	return encoded
}

func (c *Runner) Run(logger *slog.Logger) error {
	initStorage, found := storage.GetFromDSN(c.Storage)
	if !found {
		return fmt.Errorf("could not get storage driver: %w", errors.ErrUnsupported)
	}

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
	go func() {
		sig := <-sigs
		logger.Debug("execution.canceled", "signal", sig)
		cancel() // Cancel the context when signal is received
	}()

	var pipeline string

	extension := filepath.Ext(pipelinePath)
	if extension == ".yml" || extension == ".yaml" {
		var err error

		pipeline, err = backwards.NewPipeline(pipelinePath)
		if err != nil {
			return fmt.Errorf("could not create pipeline from YAML: %w", err)
		}
	} else {
		result := api.Build(api.BuildOptions{
			EntryPoints:      []string{pipelinePath},
			Bundle:           true,
			Sourcemap:        api.SourceMapInline,
			Platform:         api.PlatformNeutral,
			PreserveSymlinks: true,
			AbsWorkingDir:    filepath.Dir(pipelinePath),
		})
		if len(result.Errors) > 0 {
			return fmt.Errorf("%w: %s", ErrCouldNotBundle, result.Errors[0].Text)
		}

		pipeline = string(result.OutputFiles[0].Contents)
	}

	// Use a unique namespace per invocation to avoid container name collisions
	// when multiple tests run the same pipeline+driver in parallel.
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	namespace := "ci-" + runtimeID + "-" + hex.EncodeToString(nonce[:])

	var driver orchestra.Driver

	switch c.Driver {
	case "docker":
		driver, err = docker.New(docker.Config{Namespace: namespace, Host: c.DockerHost}, logger)
	case "k8s":
		driver, err = k8s.New(k8s.Config{Namespace: namespace, Kubeconfig: c.K8sKubeconfig, K8sNamespace: c.K8sNamespace}, logger)
	default: // native
		driver, err = native.New(native.Config{Namespace: namespace}, logger)
	}

	if err != nil {
		return fmt.Errorf("could not create orchestrator client (%q): %w", c.Driver, err)
	}

	defer func() { _ = driver.Close() }()

	if c.CacheURL != "" {
		params := map[string]string{"cache": c.CacheURL}
		if c.CacheCompression != "" {
			params["cache_compression"] = c.CacheCompression
		}
		if c.CachePrefix != "" {
			params["cache_prefix"] = c.CachePrefix
		}

		driver, err = cache.WrapWithCaching(driver, params, logger)
		if err != nil {
			return fmt.Errorf("could not initialize cache layer: %w", err)
		}
	}

	storage, err := initStorage(c.Storage, runtimeID, logger)
	if err != nil {
		return fmt.Errorf("could not create sqlite client: %w", err)
	}
	defer func() { _ = storage.Close() }()

	// Initialize secrets manager if configured
	var secretsManager secrets.Manager

	if c.Secrets != "" {
		secretsManager, err = secrets.GetFromDSN(c.Secrets, logger)
		if err != nil {
			return fmt.Errorf("could not create secrets manager: %w", err)
		}
		defer func() { _ = secretsManager.Close() }()

		// Store any secrets provided via --secret flags (pipeline scope)
		for _, s := range c.Secret {
			key, value, found := parseSecretFlag(s)
			if !found {
				return fmt.Errorf("invalid --secret flag %q: expected KEY=VALUE format", s)
			}

			err = secretsManager.Set(ctx, secrets.PipelineScope(runtimeID), key, value)
			if err != nil {
				return fmt.Errorf("could not set secret %q: %w", key, err)
			}
		}

		// Store any secrets provided via --global-secret flags (global scope)
		for _, s := range c.GlobalSecret {
			key, value, found := parseSecretFlag(s)
			if !found {
				return fmt.Errorf("invalid --global-secret flag %q: expected KEY=VALUE format", s)
			}

			err = secretsManager.Set(ctx, secrets.GlobalScope, key, value)
			if err != nil {
				return fmt.Errorf("could not set global secret %q: %w", key, err)
			}
		}
	}

	js := runtime.NewJS(logger)

	opts := runtime.ExecuteOptions{
		Resume:                c.Resume,
		RunID:                 c.RunID,
		PipelineID:            runtimeID,
		SecretsManager:        secretsManager,
		FetchTimeout:          c.FetchTimeout,
		FetchMaxResponseBytes: int64(c.FetchMaxResponseMB) * 1024 * 1024,
	}

	// If resuming but no RunID provided, use the runtime ID for consistency
	if c.Resume && opts.RunID == "" {
		opts.RunID = runtimeID
	}

	err = js.ExecuteWithOptions(ctx, pipeline, driver, storage, opts)
	if err != nil {
		// Check if the error was due to context cancellation
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

var (
	ErrCouldNotBundle       = errors.New("could not bundle pipeline")
	ErrOrchestratorNotFound = errors.New("orchestrator not found")
)
