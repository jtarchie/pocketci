package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// APIGatesController handles JSON API endpoints for approval gates.
type APIGatesController struct {
	BaseController
	allowedFeatures []Feature
}

// ListByRun handles GET /api/runs/:run_id/gates - List gates for a run.
func (c *APIGatesController) ListByRun(ctx *echo.Context) error {
	if !IsFeatureEnabled(FeatureGates, c.allowedFeatures) {
		return ctx.JSON(http.StatusForbidden, map[string]string{
			"error": "gates feature is not enabled",
		})
	}

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
	if !IsFeatureEnabled(FeatureGates, c.allowedFeatures) {
		return ctx.JSON(http.StatusForbidden, map[string]string{
			"error": "gates feature is not enabled",
		})
	}

	gateID := ctx.Param("gate_id")

	reqCtx := ctx.Request().Context()

	// Verify gate exists
	gate, err := c.store.GetGate(reqCtx, gateID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "gate not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to get gate: %v", err),
		})
	}

	if gate.Status != storage.GateStatusPending {
		return ctx.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("gate is already %s", gate.Status),
		})
	}

	approvedBy := "api"

	if err := c.store.ResolveGate(reqCtx, gateID, status, approvedBy); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.JSON(http.StatusConflict, map[string]string{
				"error": "gate was already resolved",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to resolve gate: %v", err),
		})
	}

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

// RegisterRoutes registers all gate API routes on the given group.
func (c *APIGatesController) RegisterRoutes(api *echo.Group) {
	api.GET("/runs/:run_id/gates", c.ListByRun)
	api.POST("/gates/:gate_id/approve", c.Approve)
	api.POST("/gates/:gate_id/reject", c.Reject)
}
