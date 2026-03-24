package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jtarchie/pocketci/cache"
	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/jtarchie/pocketci/storage"
	"github.com/jtarchie/pocketci/webhooks"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// ObservabilityProvider is the interface used by the router for optional
// observability backends (e.g., PostHog, Honeybadger). Kept as a local
// interface to avoid importing the observability package directly.
type ObservabilityProvider interface {
	Event(eventType string, data map[string]any) error
}

// RouterOptions configures the router.
type RouterOptions struct {
	MaxInFlight           int
	WebhookTimeout        time.Duration
	BasicAuthUsername     string
	BasicAuthPassword     string
	AllowedDrivers        string
	AllowedFeatures       string
	SecretsManager        secrets.Manager
	FetchTimeout          time.Duration
	FetchMaxResponseBytes int64
	AuthConfig            *auth.Config
	ObservabilityProvider ObservabilityProvider
	// DefaultDriver is the name of the default driver when a pipeline doesn't specify one.
	DefaultDriver string
	// DriverConfigs maps driver names to their typed server configurations.
	// Every driver the server is willing to serve should have an entry.
	DriverConfigs map[string]orchestra.DriverConfig
	// CacheStore is the optional cache backend. When non-nil every created
	// driver is wrapped with caching.
	CacheStore cache.CacheStore
	// CacheCompression is the compression algorithm for the cache (zstd, gzip, none).
	CacheCompression string
	// CacheKeyPrefix is prepended to all cache keys.
	CacheKeyPrefix string
	// WebhookProviders is the ordered list of webhook providers to use for detection.
	// Providers are checked in order; the first match wins.
	WebhookProviders []webhooks.Provider
}

// Router wraps echo.Echo and provides access to the execution service.
type Router struct {
	*echo.Echo
	execService     *ExecutionService
	webGroup        *echo.Group
	allowedDrivers  []string
	allowedFeatures []Feature
}

// WaitForExecutions blocks until all in-flight pipeline executions have completed.
// This is useful for graceful shutdown or testing.
func (r *Router) WaitForExecutions() {
	r.execService.Wait()
}

// ExecutionService returns the execution service for testing purposes.
func (r *Router) ExecutionService() *ExecutionService {
	return r.execService
}

// ProtectedGroup returns the web group that has basic auth middleware applied.
// Use this to add routes that should require authentication.
func (r *Router) ProtectedGroup() *echo.Group {
	return r.webGroup
}

// isHtmxRequest checks if the request is from htmx.
func isHtmxRequest(ctx *echo.Context) bool {
	return ctx.Request().Header.Get("HX-Request") == "true"
}

// newBasicAuthMiddleware creates a basic auth middleware using Echo's built-in BasicAuth.
// If username/password are empty strings, the middleware is disabled (returns a no-op middleware).
// newSlogMiddleware creates a request-logging middleware using slog.
func newSlogMiddleware(logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()

			err := next(c)

			// Read from the post-handler request — auth middlewares enrich
			// the context during next(c), so a single read captures everything.
			req := c.Request()
			level := slog.LevelInfo

			attrs := []slog.Attr{
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.Duration("latency", time.Since(start)),
				slog.String("remote_ip", c.RealIP()),
			}

			requestID, _ := RequestIDFromContext(req.Context())
			if requestID == "" {
				requestID = c.Response().Header().Get(echo.HeaderXRequestID)
			}
			if requestID != "" {
				attrs = append(attrs, slog.String("request_id", requestID))
			}

			if actor, ok := RequestActorFromContext(req.Context()); ok {
				attrs = append(attrs,
					slog.String("auth_provider", actor.Provider),
					slog.String("user", actor.User),
				)
			}

			if err != nil {
				level = slog.LevelError
				attrs = append(attrs, slog.String("error", err.Error()))

				// Use the HTTP error code if Echo provides one.
				var he *echo.HTTPError
				if errors.As(err, &he) {
					attrs = append(attrs, slog.Int("status", he.Code))
				} else {
					attrs = append(attrs, slog.Int("status", http.StatusInternalServerError))
				}
			} else if resp, rErr := echo.UnwrapResponse(c.Response()); rErr == nil {
				attrs = append(attrs, slog.Int("status", resp.Status))
			}

			logger.LogAttrs(req.Context(), level, "request", attrs...)

			return err
		}
	}
}

func newBasicAuthMiddleware(username, password string) echo.MiddlewareFunc {
	if username == "" || password == "" {
		// No basic auth configured, return a no-op middleware
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	return middleware.BasicAuth(func(c *echo.Context, u, p string) (bool, error) {
		authed := u == username && p == password
		if authed {
			req := c.Request()
			req = req.WithContext(auth.WithRequestActor(req.Context(), auth.RequestActor{Provider: "basic", User: u}))
			c.SetRequest(req)
		}

		return authed, nil
	})
}

func NewRouter(logger *slog.Logger, store storage.Driver, opts RouterOptions) (*Router, error) {
	router := echo.New()

	// Parse allowed drivers
	allowedDrivers := parseAllowedDrivers(opts.AllowedDrivers)

	// Parse allowed features
	allowedFeatures, err := ParseAllowedFeatures(opts.AllowedFeatures)
	if err != nil {
		return nil, fmt.Errorf("could not parse allowed features: %w", err)
	}

	// Create execution service with allowed drivers and features
	execService := NewExecutionService(store, logger, opts.MaxInFlight, allowedDrivers)
	execService.SecretsManager = opts.SecretsManager
	execService.AllowedFeatures = allowedFeatures
	execService.FetchTimeout = opts.FetchTimeout
	execService.FetchMaxResponseBytes = opts.FetchMaxResponseBytes
	execService.DriverConfigs = opts.DriverConfigs
	execService.CacheStore = opts.CacheStore
	execService.CacheCompression = opts.CacheCompression
	execService.CacheKeyPrefix = opts.CacheKeyPrefix

	if opts.DefaultDriver != "" {
		execService.DefaultDriver = opts.DefaultDriver
	}

	// Recover orphaned runs from previous server instance
	execService.RecoverOrphanedRuns(context.Background())

	router.Use(middleware.RequestIDWithConfig(middleware.RequestIDConfig{
		RequestIDHandler: func(c *echo.Context, id string) {
			req := c.Request()
			req = req.WithContext(requestContextWithRequestID(req.Context(), id))
			c.SetRequest(req)
		},
	}))
	router.Use(newSlogMiddleware(logger))
	router.Use(middleware.Recover())

	renderer, err := newTemplates()
	if err != nil {
		return nil, fmt.Errorf("could not create templates: %w", err)
	}

	router.Renderer = renderer

	// Serve static files from embedded filesystem
	staticFiles, err := fs.Sub(staticFS, "static/dist")
	if err != nil {
		return nil, fmt.Errorf("could not create static files: %w", err)
	}
	router.GET("/static/*", echo.WrapHandler(http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles)))))

	docsFiles, err := fs.Sub(docsFS, "docs/site")
	if err != nil {
		return nil, fmt.Errorf("could not create docs files: %w", err)
	}
	router.GET("/docs", func(ctx *echo.Context) error {
		return ctx.Redirect(http.StatusMovedPermanently, "/docs/")
	})
	router.GET("/docs/*", echo.WrapHandler(http.StripPrefix("/docs/", http.FileServer(http.FS(docsFiles)))))

	router.GET("/health", func(ctx *echo.Context) error {
		return ctx.String(http.StatusOK, "OK")
	})
	router.GET("/health/", func(ctx *echo.Context) error {
		return ctx.String(http.StatusOK, "OK")
	})

	// Create web UI group and apply auth middleware
	web := router.Group("")

	// Determine auth strategy: OAuth, basic auth, or open access
	if opts.AuthConfig != nil && opts.AuthConfig.HasOAuthProviders() {
		// Initialize OAuth providers and session store
		auth.InitProviders(opts.AuthConfig)
		sessionStore := auth.SessionStore(opts.AuthConfig.SessionSecret)
		tokenValidator := auth.TokenValidator(opts.AuthConfig.SessionSecret)

		// Create OAuth authorization server for MCP clients.
		oauthSrv := auth.NewOAuthServer(opts.AuthConfig, sessionStore, logger)

		// Register OAuth routes (unauthenticated), including /oauth/authorize and /oauth/token.
		auth.RegisterRoutes(router, opts.AuthConfig, sessionStore, logger, oauthSrv)

		// Mount well-known metadata endpoints (unauthenticated).
		baseURL := opts.AuthConfig.CallbackURL
		resourceMeta := auth.NewProtectedResourceMetadata(baseURL)
		authServerMeta := auth.NewAuthServerMetadata(baseURL)
		router.GET("/.well-known/oauth-protected-resource", echo.WrapHandler(auth.ProtectedResourceMetadataHandler(resourceMeta)))
		router.GET("/.well-known/oauth-authorization-server", echo.WrapHandler(auth.AuthServerMetadataHandler(authServerMeta)))

		// Apply auth middleware to web and API groups
		authMiddleware := auth.RequireAuth(opts.AuthConfig, sessionStore, tokenValidator, logger)
		web.Use(authMiddleware)

		// Apply server-level RBAC if configured
		if opts.AuthConfig.ServerRBAC != "" {
			web.Use(auth.RequireRBAC(opts.AuthConfig.ServerRBAC, logger))
		}

		// MCP endpoint — protected with MCP SDK bearer token middleware.
		mcpHandler := newMCPHandler(store)
		verifier := auth.MCPTokenVerifier(opts.AuthConfig.SessionSecret)
		bearerOpts := &mcpauth.RequireBearerTokenOptions{
			ResourceMetadataURL: baseURL + "/.well-known/oauth-protected-resource",
			Scopes:              []string{auth.MCPScope},
		}
		protectedMCP := mcpauth.RequireBearerToken(verifier, bearerOpts)(mcpHandler)
		router.Any("/mcp", echo.WrapHandler(protectedMCP))
		router.Any("/mcp/*", echo.WrapHandler(protectedMCP))
	} else {
		web.Use(newBasicAuthMiddleware(opts.BasicAuthUsername, opts.BasicAuthPassword))

		// MCP endpoint — behind basic auth middleware on the web group.
		mcpHandler := newMCPHandler(store)
		web.Any("/mcp", echo.WrapHandler(mcpHandler))
		web.Any("/mcp/*", echo.WrapHandler(mcpHandler))
	}

	// Redirect root to pipelines list
	web.GET("/", func(ctx *echo.Context) error {
		return ctx.Redirect(http.StatusMovedPermanently, "/pipelines/")
	})

	webhookTimeout := opts.WebhookTimeout
	if webhookTimeout == 0 {
		webhookTimeout = 5 * time.Second
	}

	// Create API group with auth middleware (for non-webhook endpoints)
	api := router.Group("/api")

	if opts.AuthConfig != nil && opts.AuthConfig.HasOAuthProviders() {
		sessionStore := auth.SessionStore(opts.AuthConfig.SessionSecret)
		tokenValidator := auth.TokenValidator(opts.AuthConfig.SessionSecret)
		api.Use(auth.RequireAuth(opts.AuthConfig, sessionStore, tokenValidator, logger))

		if opts.AuthConfig.ServerRBAC != "" {
			api.Use(auth.RequireRBAC(opts.AuthConfig.ServerRBAC, logger))
		}
	} else {
		api.Use(newBasicAuthMiddleware(opts.BasicAuthUsername, opts.BasicAuthPassword))
	}

	configuredDrivers := make([]string, 0, len(opts.DriverConfigs))
	for name := range opts.DriverConfigs {
		configuredDrivers = append(configuredDrivers, name)
	}

	registerRoutes(router, api, web, store, execService, allowedDrivers, configuredDrivers, allowedFeatures, opts.SecretsManager, opts.WebhookProviders, webhookTimeout, logger)

	return &Router{Echo: router, execService: execService, webGroup: web, allowedDrivers: allowedDrivers, allowedFeatures: allowedFeatures}, nil
}

// registerRoutes wires all controllers to their respective route groups.
func registerRoutes(
	router *echo.Echo,
	api *echo.Group,
	web *echo.Group,
	store storage.Driver,
	execService *ExecutionService,
	allowedDrivers []string,
	configuredDrivers []string,
	allowedFeatures []Feature,
	secretsMgr secrets.Manager,
	webhookProviders []webhooks.Provider,
	webhookTimeout time.Duration,
	logger *slog.Logger,
) {
	base := BaseController{store: store, execService: execService}

	// API controllers (JSON responses)
	(&APIPipelinesController{BaseController: base, logger: logger, allowedDrivers: allowedDrivers, allowedFeatures: allowedFeatures, secretsMgr: secretsMgr}).RegisterRoutes(api)
	(&APIRunsController{BaseController: base, allowedFeatures: allowedFeatures}).RegisterRoutes(api)
	(&APIDriversController{allowedDrivers: allowedDrivers, configuredDrivers: configuredDrivers}).RegisterRoutes(api)
	(&APIFeaturesController{allowedFeatures: allowedFeatures}).RegisterRoutes(api)

	// Webhooks registered on the main router (no auth group, before API group)
	(&APIWebhooksController{BaseController: base, allowedFeatures: allowedFeatures, webhookTimeout: webhookTimeout, logger: logger.WithGroup("webhook"), secretsMgr: secretsMgr, providers: webhookProviders}).RegisterRoutes(router)

	// Web controllers (HTML responses)
	(&WebPipelinesController{BaseController: base, secretsMgr: secretsMgr}).RegisterRoutes(web)
	(&WebRunsController{BaseController: base}).RegisterRoutes(web)
	(&WebMetricsController{BaseController: base}).RegisterRoutes(web)

	// Share controllers (public web + authenticated API)
	webShare, apiShare := newShareControllers(base, secretsMgr, logger)
	webShare.RegisterRoutes(router)
	apiShare.RegisterRoutes(api)
}
