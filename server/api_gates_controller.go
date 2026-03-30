package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// APIGatesController handles JSON API endpoints for approval gates.
type APIGatesController struct {
	BaseController
	allowedFeatures []Feature
	logger          *slog.Logger
}

// ListByRun handles GET /api/runs/:run_id/gates - List gates for a run.
func (c *APIGatesController) ListByRun(ctx *echo.Context) error {
	runID := ctx.Param("run_id")

	gates, err := c.store.GetGatesByRunID(ctx.Request().Context(), runID)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get gates: %v", err),
		})
	}

	return ctx.JSON(http.StatusOK, gates)
}

// Approve handles POST /api/gates/:gate_id/approve - Approve a pending gate.
func (c *APIGatesController) Approve(ctx *echo.Context) error {
	return c.resolveGate(ctx, storage.GateStatusApproved)
}

// Reject handles POST /api/gates/:gate_id/reject - Reject a pending gate.
func (c *APIGatesController) Reject(ctx *echo.Context) error {
	return c.resolveGate(ctx, storage.GateStatusRejected)
}

func (c *APIGatesController) resolveGate(ctx *echo.Context, status storage.GateStatus) error {
	gateID := ctx.Param("gate_id")

	reqCtx := ctx.Request().Context()
	approvedBy := "api"

	// ResolveGate atomically updates only pending gates (WHERE status = 'pending'),
	// so no pre-fetch is needed, avoiding a TOCTOU race.
	if err := c.store.ResolveGate(reqCtx, gateID, status, approvedBy); err != nil {
		c.logger.Error("gate.resolve.failed",
			slog.String("gate_id", gateID),
			slog.String("status", string(status)),
			slog.Any("error", err),
		)
		if errors.Is(err, storage.ErrNotFound) {
			// Distinguish "gate doesn't exist" from "gate already resolved".
			gate, getErr := c.store.GetGate(reqCtx, gateID)
			if getErr != nil {
				return ctx.JSON(http.StatusNotFound, map[string]string{
					"error": "gate not found",
				})
			}

			return ctx.JSON(http.StatusConflict, map[string]string{
				"error": fmt.Sprintf("gate is already %s", gate.Status),
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to resolve gate: %v", err),
		})
	}

	c.logger.Info("gate.resolved",
		slog.String("gate_id", gateID),
		slog.String("status", string(status)),
		slog.String("approved_by", approvedBy),
	)

	if isHtmxRequest(ctx) {
		action := "approved"
		if status == storage.GateStatusRejected {
			action = "rejected"
		}

		ctx.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast":{"message":"Gate %s","type":"success"}}`, action))

		return ctx.NoContent(http.StatusOK)
	}

	return ctx.JSON(http.StatusOK, map[string]string{
		"gate_id": gateID,
		"status":  string(status),
	})
}

// requireGatesFeature is middleware that rejects requests when the gates feature is disabled.
func (c *APIGatesController) requireGatesFeature(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx *echo.Context) error {
		if !IsFeatureEnabled(FeatureGates, c.allowedFeatures) {
			return ctx.JSON(http.StatusForbidden, map[string]string{
				"error": "gates feature is not enabled",
			})
		}

		return next(ctx)
	}
}

// RegisterRoutes registers all gate API routes on the given group.
func (c *APIGatesController) RegisterRoutes(api *echo.Group) {
	g := api.Group("", c.requireGatesFeature)
	g.GET("/runs/:run_id/gates", c.ListByRun)
	g.POST("/gates/:gate_id/approve", c.Approve)
	g.POST("/gates/:gate_id/reject", c.Reject)
}
