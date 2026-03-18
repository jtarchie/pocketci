package orchestra

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// DriverFactory creates a new Driver instance from a flat config map and logger.
// The namespace is set by the caller for per-execution isolation.
type DriverFactory func(namespace string, config map[string]string, logger *slog.Logger) (Driver, error)

// registry is the explicit driver name → factory mapping.
// Populated by RegisterDriver at init-time from the individual driver packages.
var registry = map[string]DriverFactory{}

// RegisterDriver adds a driver factory to the registry.
// Called from driver package init() functions.
func RegisterDriver(name string, factory DriverFactory) {
	registry[name] = factory
}

// CreateDriver creates a new driver by name using the registry.
func CreateDriver(name, namespace string, config map[string]string, logger *slog.Logger) (Driver, error) {
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", name)
	}

	return factory(namespace, config, logger)
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
