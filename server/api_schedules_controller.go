package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// APISchedulesController handles schedule-specific API endpoints.
type APISchedulesController struct {
	store storage.Driver
}

// UpdateEnabled handles PUT /api/schedules/:id/enabled - Enable or disable a schedule.
func (c *APISchedulesController) UpdateEnabled(ctx *echo.Context) error {
	id := ctx.Param("id")

	var body struct {
		Enabled bool `json:"enabled"`
	}

	if err := json.NewDecoder(ctx.Request().Body).Decode(&body); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("invalid request body: %v", err),
		})
	}

	err := c.store.UpdateScheduleEnabled(ctx.Request().Context(), id, body.Enabled)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "schedule not found",
			})
		}

		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to update schedule: %v", err),
		})
	}

	return ctx.JSON(http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// RegisterScheduleRoutes registers the schedule API routes.
func RegisterScheduleRoutes(api *echo.Group, store storage.Driver) {
	ctrl := &APISchedulesController{store: store}
	api.PUT("/schedules/:id/enabled", ctrl.UpdateEnabled)
}
