package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v5"
	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
)

// RegisterRoutes adds OAuth authentication routes to the Echo router.
// These routes are NOT behind auth middleware — they ARE the auth flow.
// If oauthSrv is non-nil, OAuth authorization code endpoints are also registered.
func RegisterRoutes(router *echo.Echo, cfg *Config, store *sessions.CookieStore, logger *slog.Logger, oauthSrv *OAuthServer) {
	gothic.Store = store

	h := &authHandler{
		cfg:      cfg,
		store:    store,
		logger:   logger,
		cliCodes: make(map[string]*cliLoginState),
		oauthSrv: oauthSrv,
	}

	router.GET("/auth/login", h.LoginPage)
	router.GET("/auth/logout", h.Logout)
	router.GET("/auth/:provider", h.BeginAuth)
	router.GET("/auth/:provider/callback", h.Callback)
	router.GET("/auth/user", h.CurrentUser)

	// CLI device flow endpoints.
	router.POST("/auth/cli/begin", h.CLIBegin)
	router.GET("/auth/cli/poll", h.CLIPoll)
	router.GET("/auth/cli/approve", h.CLIApprove)

	// OAuth authorization server endpoints (for MCP clients).
	if oauthSrv != nil {
		router.GET("/oauth/authorize", echo.WrapHandler(http.HandlerFunc(oauthSrv.HandleAuthorize)))
		router.POST("/oauth/token", echo.WrapHandler(http.HandlerFunc(oauthSrv.HandleToken)))
		router.POST("/oauth/register", echo.WrapHandler(http.HandlerFunc(oauthSrv.HandleRegister)))
	}
}

type cliLoginState struct {
	Code      string
	User      *User
	Token     string
	ExpiresAt time.Time
	Approved  bool
}

type authHandler struct {
	cfg      *Config
	store    *sessions.CookieStore
	logger   *slog.Logger
	oauthSrv *OAuthServer

	mu       sync.Mutex
	cliCodes map[string]*cliLoginState
}

func (h *authHandler) LoginPage(c *echo.Context) error {
	return c.Render(http.StatusOK, "login.html", map[string]any{
		"Title":     "Login",
		"Providers": h.cfg.EnabledProviders(),
	})
}

func (h *authHandler) Logout(c *echo.Context) error {
	if err := ClearSession(c.Response(), c.Request(), h.store); err != nil {
		h.logger.Error("auth.logout.error", "error", err)
	}

	h.logger.Info("auth.logout")

	return c.Redirect(http.StatusFound, "/auth/login")
}

func (h *authHandler) BeginAuth(c *echo.Context) error {
	provider := c.Param("provider")

	req := c.Request()
	q := req.URL.Query()
	q.Set("provider", provider)
	req.URL.RawQuery = q.Encode()

	gothic.BeginAuthHandler(c.Response(), req)

	return nil
}

func (h *authHandler) Callback(c *echo.Context) error {
	provider := c.Param("provider")

	req := c.Request()
	q := req.URL.Query()
	q.Set("provider", provider)
	req.URL.RawQuery = q.Encode()

	gothUser, err := gothic.CompleteUserAuth(c.Response(), req)
	if err != nil {
		h.logger.Error("auth.callback.error", "error", err, "provider", provider)
		return c.Redirect(http.StatusFound, "/auth/login?error=auth_failed")
	}

	user := fromGothUser(gothUser)

	// Enrich with GitHub organizations if applicable.
	if provider == "github" && gothUser.AccessToken != "" {
		orgs, orgErr := fetchGitHubOrgs(c.Request().Context(), gothUser.AccessToken)
		if orgErr != nil {
			h.logger.Warn("auth.github.orgs.error", "error", orgErr)
		} else {
			user.Organizations = orgs
		}
	}

	if err := SaveUserToSession(c.Response(), c.Request(), h.store, user); err != nil {
		h.logger.Error("auth.session.save.error", "error", err)
		return c.Redirect(http.StatusFound, "/auth/login?error=session_failed")
	}

	h.logger.Info("auth.login.success",
		"email", user.Email,
		"provider", user.Provider,
		"name", user.Name,
	)

	// Check if this was triggered by a CLI login flow.
	cliCode := c.QueryParam("cli_code")
	if cliCode != "" {
		h.mu.Lock()
		state, ok := h.cliCodes[cliCode]
		if ok && time.Now().Before(state.ExpiresAt) {
			state.User = user
			state.Approved = true
		}
		h.mu.Unlock()

		if ok {
			return c.Render(http.StatusOK, "cli_approve.html", map[string]any{
				"Approved": true,
			})
		}
	}

	// Check if this was triggered by an MCP OAuth authorization flow.
	if h.oauthSrv != nil && h.oauthSrv.CompleteAuthorize(c.Response(), c.Request(), user) {
		return nil
	}

	return c.Redirect(http.StatusFound, "/")
}

func (h *authHandler) CurrentUser(c *echo.Context) error {
	user := GetUserFromSession(c.Request(), h.store)
	if user == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "not authenticated",
		})
	}

	return c.JSON(http.StatusOK, user)
}

// CLIBegin starts a CLI device flow login. Returns a one-time code and URL.
func (h *authHandler) CLIBegin(c *echo.Context) error {
	code, err := generateRandomCode()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "could not generate login code",
		})
	}

	h.mu.Lock()
	h.cliCodes[code] = &cliLoginState{
		Code:      code,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	h.mu.Unlock()

	// Clean up expired codes periodically.
	go h.cleanupExpiredCodes()

	loginURL := fmt.Sprintf("%s/auth/cli/approve?code=%s", h.cfg.CallbackURL, code)

	return c.JSON(http.StatusOK, map[string]string{
		"code":      code,
		"login_url": loginURL,
	})
}

// CLIApprove renders a page where the user selects their OAuth provider to approve the CLI login.
func (h *authHandler) CLIApprove(c *echo.Context) error {
	code := c.QueryParam("code")

	h.mu.Lock()
	state, ok := h.cliCodes[code]
	h.mu.Unlock()

	if !ok || time.Now().After(state.ExpiresAt) {
		return c.String(http.StatusBadRequest, "Invalid or expired login code")
	}

	// If user already has a valid session, approve directly.
	user := GetUserFromSession(c.Request(), h.store)
	if user != nil {
		h.mu.Lock()
		state.User = user
		state.Approved = true
		h.mu.Unlock()

		return c.Render(http.StatusOK, "cli_approve.html", map[string]any{
			"Approved": true,
		})
	}

	// Show login page with the code passed through.
	return c.Render(http.StatusOK, "cli_approve.html", map[string]any{
		"Approved":  false,
		"Code":      code,
		"Providers": h.cfg.EnabledProviders(),
	})
}

// CLIPoll checks if the CLI login has been approved. Returns token if approved.
func (h *authHandler) CLIPoll(c *echo.Context) error {
	code := c.QueryParam("code")

	h.mu.Lock()
	state, ok := h.cliCodes[code]
	h.mu.Unlock()

	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "unknown code",
		})
	}

	if time.Now().After(state.ExpiresAt) {
		h.mu.Lock()
		delete(h.cliCodes, code)
		h.mu.Unlock()

		return c.JSON(http.StatusGone, map[string]string{
			"error": "code expired",
		})
	}

	if !state.Approved || state.User == nil {
		return c.JSON(http.StatusAccepted, map[string]string{
			"status": "pending",
		})
	}

	// Generate API token for the CLI (no scope restriction).
	token, err := GenerateToken(state.User, h.cfg.SessionSecret, 30*24*time.Hour, nil)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "could not generate token",
		})
	}

	// Remove the code — it's been consumed.
	h.mu.Lock()
	delete(h.cliCodes, code)
	h.mu.Unlock()

	h.logger.Info("auth.cli.login.success",
		"email", state.User.Email,
		"provider", state.User.Provider,
	)

	return c.JSON(http.StatusOK, map[string]string{
		"status": "approved",
		"token":  token,
	})
}

func (h *authHandler) cleanupExpiredCodes() {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	for code, state := range h.cliCodes {
		if now.After(state.ExpiresAt) {
			delete(h.cliCodes, code)
		}
	}
}

func fromGothUser(gu goth.User) *User {
	return &User{
		Email:     gu.Email,
		Name:      gu.Name,
		NickName:  gu.NickName,
		AvatarURL: gu.AvatarURL,
		Provider:  gu.Provider,
		UserID:    gu.UserID,
		RawData:   gu.RawData,
	}
}

// fetchGitHubOrgs fetches the user's GitHub organization memberships using their access token.
func fetchGitHubOrgs(ctx context.Context, accessToken string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/orgs?per_page=100", nil)
	if err != nil {
		return nil, fmt.Errorf("could not create orgs request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not fetch orgs: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github orgs API returned status %d", resp.StatusCode)
	}

	var orgs []struct {
		Login string `json:"login"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&orgs); err != nil {
		return nil, fmt.Errorf("could not decode orgs: %w", err)
	}

	result := make([]string, 0, len(orgs))
	for _, org := range orgs {
		result = append(result, org.Login)
	}

	return result, nil
}
