package server

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"
)

// APIFeaturesController handles JSON API endpoints for listing available features.
type APIFeaturesController struct {
	allowedFeatures []Feature
}

// Index handles GET /api/features - List allowed features.
func (c *APIFeaturesController) Index(ctx *echo.Context) error {
	features := make([]string, len(c.allowedFeatures))
	for i, f := range c.allowedFeatures {
		features[i] = string(f)
	}

	err := ctx.JSON(http.StatusOK, map[string]any{
		"features": features,
	})
	if err != nil {
		return fmt.Errorf("features response: %w", err)
	}

	return nil
}

// RegisterRoutes registers all feature API routes on the given group.
func (c *APIFeaturesController) RegisterRoutes(api *echo.Group) {
	api.GET("/features", c.Index)
}
