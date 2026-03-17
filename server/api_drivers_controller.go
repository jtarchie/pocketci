package server

import (
	"net/http"

	"github.com/labstack/echo/v5"
)

// APIDriversController handles JSON API endpoints for listing available drivers.
type APIDriversController struct {
	allowedDrivers []string
	driverName     string
}

// Index handles GET /api/drivers - List allowed drivers.
func (c *APIDriversController) Index(ctx *echo.Context) error {
	var drivers []string

	if len(c.allowedDrivers) == 1 && c.allowedDrivers[0] == "*" {
		if c.driverName != "" {
			drivers = []string{c.driverName}
		}
	} else {
		drivers = c.allowedDrivers
	}

	return ctx.JSON(http.StatusOK, map[string]any{
		"drivers": drivers,
	})
}

// RegisterRoutes registers all driver API routes on the given group.
func (c *APIDriversController) RegisterRoutes(api *echo.Group) {
	api.GET("/drivers", c.Index)
}
