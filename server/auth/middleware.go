package auth

import (
	"encoding/gob"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v5"
)

const sessionName = "pocketci_session"

func init() {
	gob.Register(&User{})
}

// SessionStore creates a gorilla cookie store configured for secure session management.
func SessionStore(secret string) *sessions.CookieStore {
	store := sessions.NewCookieStore([]byte(secret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400, // 24 hours
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}

	return store
}

// GetUserFromSession reads the authenticated user from the session cookie.
// Returns nil if no user is stored in the session.
func GetUserFromSession(r *http.Request, store *sessions.CookieStore) *User {
	session, err := store.Get(r, sessionName)
	if err != nil {
		return nil
	}

	user, ok := session.Values["user"].(*User)
	if !ok {
		return nil
	}

	return user
}

// SaveUserToSession stores the authenticated user in the session cookie.
func SaveUserToSession(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, user *User) error {
	session, err := store.Get(r, sessionName)
	if err != nil {
		return err
	}

	session.Values["user"] = user

	return session.Save(r, w)
}

// ClearSession removes the user from the session cookie.
func ClearSession(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore) error {
	session, err := store.Get(r, sessionName)
	if err != nil {
		return err
	}

	session.Options.MaxAge = -1

	return session.Save(r, w)
}

// RequireAuth creates an Echo middleware that enforces authentication.
// It checks (in order): Bearer token, session cookie.
// If no valid auth is found, it redirects browsers to /auth/login or returns 401 for API requests.
// If authConfig has no OAuth providers, it returns a no-op middleware (open access).
func RequireAuth(cfg *Config, store *sessions.CookieStore, tokenValidator func(string) (*User, error), logger *slog.Logger) echo.MiddlewareFunc {
	if !cfg.HasOAuthProviders() {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Check Bearer token (for CLI/API access).
			if authHeader := c.Request().Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

				user, err := tokenValidator(tokenStr)
				if err != nil {
					logger.Warn("auth.token.invalid", "error", err)

					return c.JSON(http.StatusUnauthorized, map[string]string{
						"error": "invalid or expired token",
					})
				}

				c.Set(string(userContextKey), user)
				req := c.Request()
				req = req.WithContext(WithRequestActor(req.Context(), actorFromOAuthUser(user)))
				c.SetRequest(req)

				return next(c)
			}

			// Check session cookie.
			user := GetUserFromSession(c.Request(), store)
			if user != nil {
				c.Set(string(userContextKey), user)
				req := c.Request()
				req = req.WithContext(WithRequestActor(req.Context(), actorFromOAuthUser(user)))
				c.SetRequest(req)
				return next(c)
			}

			// Not authenticated — redirect browsers, 401 for API.
			logger.Debug("auth.unauthenticated", "path", c.Request().URL.Path)

			if isAPIRequest(c) {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "authentication required",
				})
			}

			return c.Redirect(http.StatusFound, "/auth/login")

		}
	}
}

// RequireRBAC creates an Echo middleware that enforces server-level RBAC.
// The expression is evaluated against the authenticated user (set by RequireAuth).
// An empty expression allows all authenticated users.
func RequireRBAC(expression string, logger *slog.Logger) echo.MiddlewareFunc {
	if expression == "" {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			user := GetUser(c)
			if user == nil {
				// No user in context — RequireAuth should run first.
				if isAPIRequest(c) {
					return c.JSON(http.StatusUnauthorized, map[string]string{
						"error": "authentication required",
					})
				}

				return c.Render(http.StatusUnauthorized, "error.html", map[string]any{
					"Title":   "Authentication Required",
					"Message": "Please sign in to access this page.",
				})
			}

			allowed, err := EvaluateAccess(expression, *user)
			if err != nil {
				logger.Error("rbac.eval.error", "error", err, "user", user.Email)

				if isAPIRequest(c) {
					return c.JSON(http.StatusForbidden, map[string]string{
						"error": "access denied",
					})
				}

				return c.Render(http.StatusForbidden, "error.html", map[string]any{
					"Title":   "Access Denied",
					"Message": "You do not have permission to access this page.",
				})
			}

			if !allowed {
				logger.Warn("rbac.access.denied", "user", user.Email, "provider", user.Provider)

				if isAPIRequest(c) {
					return c.JSON(http.StatusForbidden, map[string]string{
						"error": "access denied",
					})
				}

				return c.Render(http.StatusForbidden, "error.html", map[string]any{
					"Title":   "Access Denied",
					"Message": "You do not have permission to access this page.",
				})
			}

			return next(c)
		}
	}
}

// GetUser retrieves the authenticated user from the Echo context.
// Returns nil if no user is authenticated.
func GetUser(c *echo.Context) *User {
	val := c.Get(string(userContextKey))
	if val == nil {
		return nil
	}

	user, ok := val.(*User)
	if !ok {
		return nil
	}

	return user
}

func isAPIRequest(c *echo.Context) bool {
	accept := c.Request().Header.Get("Accept")

	return strings.Contains(accept, "application/json") ||
		strings.HasPrefix(c.Request().URL.Path, "/api/")
}
