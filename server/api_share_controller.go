package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jtarchie/pocketci/secrets"
	"github.com/jtarchie/pocketci/server/auth"
	"github.com/labstack/echo/v5"
)

// APIShareController handles share token generation for pipeline runs.
type APIShareController struct {
	BaseController
	secretsMgr    secrets.Manager
	signingSecret string
	logger        *slog.Logger
}

// CreateShare handles POST /api/runs/:run_id/share.
// It verifies the run exists, then returns a signed share URL.
func (c *APIShareController) CreateShare(ctx *echo.Context) error {
	runID := ctx.Param("run_id")

	_, err := c.store.GetRun(ctx.Request().Context(), runID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "run not found")
	}

	token, err := auth.GenerateShareToken(runID, c.signingSecret)
	if err != nil {
		return fmt.Errorf("could not generate share token: %w", err)
	}

	return ctx.JSON(http.StatusOK, map[string]string{
		"share_path": "/share/" + token + "/tasks",
	})
}

// RegisterRoutes registers the share API route on the given authenticated group.
func (c *APIShareController) RegisterRoutes(api *echo.Group) {
	api.POST("/runs/:run_id/share", c.CreateShare)
}

// newShareControllers constructs and returns a WebShareController and
// APIShareController, resolving the signing secret once at startup.
func newShareControllers(base BaseController, mgr secrets.Manager, logger *slog.Logger) (*WebShareController, *APIShareController) {
	secret, err := resolveShareSigningSecret(context.Background(), mgr, logger)
	if err != nil {
		logger.Error("share.signing.secret.error",
			slog.String("error", err.Error()),
		)

		secret = ""
	}

	webCtrl := &WebShareController{
		WebRunsController: WebRunsController{BaseController: base},
		secretsMgr:        mgr,
		signingSecret:     secret,
		logger:            logger,
	}

	apiCtrl := &APIShareController{
		BaseController: base,
		secretsMgr:     mgr,
		signingSecret:  secret,
		logger:         logger,
	}

	return webCtrl, apiCtrl
}
