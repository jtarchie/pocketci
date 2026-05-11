package server

import (
	"fmt"
	"strings"

	"github.com/jtarchie/pocketci/storage"
	"github.com/labstack/echo/v5"
)

// BaseController holds dependencies shared by all controllers.
type BaseController struct {
	store       storage.Driver
	execService *ExecutionService
}

// parseAllowedDrivers parses a comma-separated list of driver names.
// Returns ["*"] if input is empty or "*".
// Trims whitespace from each driver name.
func parseAllowedDrivers(input string) []string {
	if input == "" || input == "*" {
		return []string{"*"}
	}

	parts := strings.Split(input, ",")
	drivers := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			drivers = append(drivers, trimmed)
		}
	}

	if len(drivers) == 0 {
		return []string{"*"}
	}

	return drivers
}

// parsePagination reads the standard "page" and "per_page" query parameters,
// falling back to (1, 20) when either is absent or unparseable. Invalid input
// is intentionally tolerated to match the existing controller contract.
func parsePagination(ctx *echo.Context) (page, perPage int) {
	page, perPage = 1, 20

	if p := ctx.QueryParam("page"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &page)
	}

	if pp := ctx.QueryParam("per_page"); pp != "" {
		_, _ = fmt.Sscanf(pp, "%d", &perPage)
	}

	return page, perPage
}

// respondHTMXOrJSON writes status with htmxMsg for HTMX clients or jsonBody
// for JSON clients. The label is used in any wrapped error returned from the
// underlying response write.
func respondHTMXOrJSON(ctx *echo.Context, status int, label, htmxMsg string, jsonBody any) error {
	if isHtmxRequest(ctx) {
		err := ctx.String(status, htmxMsg)
		if err != nil {
			return fmt.Errorf("%s htmx response: %w", label, err)
		}

		return nil
	}

	err := ctx.JSON(status, jsonBody)
	if err != nil {
		return fmt.Errorf("%s json response: %w", label, err)
	}

	return nil
}
