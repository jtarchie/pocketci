package mock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"github.com/jtarchie/pocketci/resources"
)

// Mock implements a simple mock resource for testing.
// It maintains a version counter and creates files in the destination directory.
type Mock struct {
	versionCounter atomic.Int64
}

func (m *Mock) Name() string {
	return "mock"
}

// Check returns the current version based on force_version from source.
// When no force_version is set and a previous version is provided, it generates
// a new version by incrementing the previous version number.
func (m *Mock) Check(_ context.Context, req resources.CheckRequest) (resources.CheckResponse, error) {
	forceVersion := ""
	if fv, ok := req.Source["force_version"].(string); ok {
		forceVersion = fv
	}

	if forceVersion != "" {
		// Forced version mode - always return the forced version
		version := resources.Version{
			"version": forceVersion,
		}

		if req.Version != nil && req.Version["version"] != "" {
			return resources.CheckResponse{req.Version, version}, nil
		}

		return resources.CheckResponse{version}, nil
	}

	// Dynamic version mode - generate incrementing versions
	var newVersion int64

	if req.Version != nil && req.Version["version"] != "" {
		// Parse the previous version and increment it
		var prevVersion int64

		if _, err := fmt.Sscanf(req.Version["version"], "%d", &prevVersion); err == nil {
			newVersion = prevVersion + 1
		} else {
			// If previous version isn't a number, use counter
			newVersion = m.versionCounter.Add(1)
		}
	} else {
		// First check - use counter
		newVersion = m.versionCounter.Add(1)
	}

	version := resources.Version{
		"version": strconv.FormatInt(int64(newVersion), 10),
	}

	// If a version was provided, include it and the new version
	if req.Version != nil && req.Version["version"] != "" {
		return resources.CheckResponse{req.Version, version}, nil
	}

	return resources.CheckResponse{version}, nil
}

// In creates a version file in the destination directory.
func (m *Mock) In(_ context.Context, destDir string, req resources.InRequest) (resources.InResponse, error) {
	version := req.Version["version"]
	if version == "" {
			return resources.InResponse{}, errors.New("version is required")
	}

	// Create version file
	versionFile := filepath.Join(destDir, "version")

	err := os.WriteFile(versionFile, []byte(version), 0o600)
	if err != nil {
		return resources.InResponse{}, fmt.Errorf("failed to write version file: %w", err)
	}

	// Create privileged file if requested
	if _, ok := req.Params["privileged"]; ok {
		privilegedFile := filepath.Join(destDir, "privileged")

		err = os.WriteFile(privilegedFile, []byte("true"), 0o600)
		if err != nil {
			return resources.InResponse{}, fmt.Errorf("failed to write privileged file: %w", err)
		}
	}

	return resources.InResponse{
		Version: resources.Version{
			"version": version,
		},
		Metadata: resources.Metadata{
			{Name: "version", Value: version},
		},
	}, nil
}

// Out increments the version and returns it.
func (m *Mock) Out(_ context.Context, _ string, req resources.OutRequest) (resources.OutResponse, error) {
	version := ""
	if v, ok := req.Params["version"].(string); ok {
		version = v
	} else if v, ok := req.Params["version"].(float64); ok {
		version = fmt.Sprintf("%.0f", v)
	}

	if version == "" {
		version = strconv.FormatInt(int64(m.versionCounter.Add(1)), 10)
	}

	return resources.OutResponse{
		Version: resources.Version{
			"version": version,
		},
		Metadata: resources.Metadata{
			{Name: "version", Value: version},
		},
	}, nil
}

func init() {
	resources.Register("mock", func() resources.Resource {
		return &Mock{}
	})
}

var _ resources.Resource = &Mock{}
