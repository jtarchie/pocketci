package orchestra

import (
	"fmt"
	"slices"
	"strings"
)

// DriverConfig is the interface that typed driver configurations implement.
// Each driver's ServerConfig embeds the server-level settings and identifies
// which driver it belongs to.
type DriverConfig interface {
	DriverName() string
}

// IsDriverAllowed validates that the driver name is in the allowed list.
// If allowedList contains "*", all drivers are allowed.
// Returns an error if the driver is not allowed.
func IsDriverAllowed(driver string, allowedList []string) error {
	if driver == "" {
		return fmt.Errorf("driver name is required")
	}

	// Check if wildcard (all drivers allowed)
	if slices.Contains(allowedList, "*") {
		return nil
	}

	// Check if driver is in allowed list
	if slices.Contains(allowedList, driver) {
		return nil
	}

	// Build friendly error message
	return fmt.Errorf("driver %q is not allowed on this server. Allowed drivers: %s", driver, strings.Join(allowedList, ", "))
}
