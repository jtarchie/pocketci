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
			jsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get gates: %v", err),
		})
		if jsonErr != nil {
			return fmt.Errorf("gates error response: %w", jsonErr)
		}

		return nil
	}

	err = ctx.JSON(http.StatusOK, gates)
	if err != nil {
		return fmt.Errorf("gates response: %w", err)
	}

	return nil
}

// Approve handles POST /api/gates/:gate_id/approve - Approve a pending gate.
func (c *APIGatesController) Approve(ctx *echo.Context) error {
	return c.resolveGate(ctx, storage.GateStatusApproved)
}

// Reject handles POST /api/gates/:gate_id/reject - Reject a pending gate.
func (c *APIGatesController) Reject(ctx *echo.Context) error {
	return c.resolveGate(ctx, storage.GateStatusRejected)
}

// resolveGateNotFound handles the ErrNotFound case from ResolveGate by
// determining whether the gate truly doesn't exist or is already resolved.
func (c *APIGatesController) resolveGateNotFound(ctx *echo.Context, gateID string) error {
	reqCtx := ctx.Request().Context()

	gate, getErr := c.store.GetGate(reqCtx, gateID)
	if getErr != nil {
		jsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
			"error": "gate not found",
		})
		if jsonErr != nil {
			return fmt.Errorf("gate not found response: %w", jsonErr)
		}

		return nil
	}

	jsonErr2 := ctx.JSON(http.StatusConflict, map[string]string{
		"error": fmt.Sprintf("gate is already %s", gate.Status),
	})
	if jsonErr2 != nil {
		return fmt.Errorf("gate conflict response: %w", jsonErr2)
	}

	return nil
}

func (c *APIGatesController) resolveGate(ctx *echo.Context, status storage.GateStatus) error {
	gateID := ctx.Param("gate_id")

	reqCtx := ctx.Request().Context()
	approvedBy := formatActor(ctx)

	// ResolveGate atomically updates only pending gates (WHERE status = 'pending'),
	// so no pre-fetch is needed, avoiding a TOCTOU race.
	resolveErr := c.store.ResolveGate(reqCtx, gateID, status, approvedBy)
	if resolveErr != nil {
		err := resolveErr
		c.logger.Error("gate.resolve.failed",
			slog.String("gate_id", gateID),
			slog.String("status", string(status)),
			slog.Any("error", err),
		)

		if errors.Is(err, storage.ErrNotFound) {
			// Distinguish "gate doesn't exist" from "gate already resolved".
			return c.resolveGateNotFound(ctx, gateID)
		}

		jsonErr3 := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to resolve gate: %v", err),
		})
		if jsonErr3 != nil {
			return fmt.Errorf("gate error response: %w", jsonErr3)
		}

		return nil
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

		noContentErr := ctx.NoContent(http.StatusOK)
		if noContentErr != nil {
			return fmt.Errorf("gate resolve response: %w", noContentErr)
		}

		return nil
	}

	// Return the full resolved gate for consistency with ListByRun.
	gate, err := c.store.GetGate(reqCtx, gateID)
	if err != nil {
		// Resolve succeeded but re-fetch failed; return minimal response.
		jsonErr4 := ctx.JSON(http.StatusOK, map[string]string{
			"gate_id": gateID,
			"status":  string(status),
		})
		if jsonErr4 != nil {
			return fmt.Errorf("gate minimal response: %w", jsonErr4)
		}

		return nil
	}

	jsonErr5 := ctx.JSON(http.StatusOK, gate)
	if jsonErr5 != nil {
		return fmt.Errorf("gate response: %w", jsonErr5)
	}

	return nil
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
