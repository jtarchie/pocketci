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

	decodeErr := json.NewDecoder(ctx.Request().Body).Decode(&body)
	if decodeErr != nil {
		badReqJsonErr := ctx.JSON(http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("invalid request body: %v", decodeErr),
		})
		if badReqJsonErr != nil {
			return fmt.Errorf("schedule bad request response: %w", badReqJsonErr)
		}

		return nil
	}

	err := c.store.UpdateScheduleEnabled(ctx.Request().Context(), id, body.Enabled)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			nfJsonErr := ctx.JSON(http.StatusNotFound, map[string]string{
				"error": "schedule not found",
			})
			if nfJsonErr != nil {
				return fmt.Errorf("schedule not found response: %w", nfJsonErr)
			}

			return nil
		}

		errJsonErr := ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to update schedule: %v", err),
		})
		if errJsonErr != nil {
			return fmt.Errorf("schedule error response: %w", errJsonErr)
		}

		return nil
	}

	okJsonErr := ctx.JSON(http.StatusOK, map[string]string{
		"status": "ok",
	})
	if okJsonErr != nil {
		return fmt.Errorf("schedule ok response: %w", okJsonErr)
	}

	return nil
}

// RegisterScheduleRoutes registers the schedule API routes.
func RegisterScheduleRoutes(api *echo.Group, store storage.Driver) {
	ctrl := &APISchedulesController{store: store}
	api.PUT("/schedules/:id/enabled", ctrl.UpdateEnabled)
}
