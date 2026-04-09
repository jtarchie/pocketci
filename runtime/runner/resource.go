package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jtarchie/pocketci/resources"
	"github.com/jtarchie/pocketci/runtime/support"
	"github.com/jtarchie/pocketci/secrets"
)

// ResourceRunner provides methods for executing native resources.
type ResourceRunner struct {
	ctx            context.Context //nolint: containedctx
	logger         *slog.Logger
	secretsManager secrets.Manager
	pipelineID     string
}

// NewResourceRunner creates a new ResourceRunner.
func NewResourceRunner(ctx context.Context, logger *slog.Logger) *ResourceRunner {
	return &ResourceRunner{
		ctx:    ctx,
		logger: logger.WithGroup("resource.run"),
	}
}

// SetSecretsManager configures the resource runner to resolve "secret:<KEY>"
// references in Source and Params maps before each operation.
func (r *ResourceRunner) SetSecretsManager(mgr secrets.Manager, pipelineID string) {
	r.secretsManager = mgr
	r.pipelineID = pipelineID
}

// ResourceCheckInput is the input for a Check operation from JS.
type ResourceCheckInput struct {
	Type    string            `json:"type"`
	Source  map[string]any    `json:"source"`
	Version map[string]string `json:"version,omitempty"`
}

// ResourceCheckResult is the result of a Check operation.
type ResourceCheckResult struct {
	Versions []map[string]string `json:"versions"`
}

// Check discovers new versions of a resource.
func (r *ResourceRunner) Check(input ResourceCheckInput) (*ResourceCheckResult, error) {
	logger := r.logger.With("type", input.Type, "operation", "resource.check")
	logger.Debug("resource.check")

	err := support.ResolveSecretsInMap(r.ctx, r.secretsManager, r.pipelineID, input.Source, nil)
	if err != nil {
		return nil, fmt.Errorf("could not resolve secrets in source: %w", err)
	}

	res, err := resources.Get(input.Type)
	if err != nil {
		return nil, fmt.Errorf("resource type not found: %w", err)
	}

	req := resources.CheckRequest{
		Source:  input.Source,
		Version: input.Version,
	}

	resp, err := res.Check(r.ctx, req)
	if err != nil {
		logger.Error("resource.check.failed", "err", err)

		return nil, fmt.Errorf("check failed: %w", err)
	}

	versions := make([]map[string]string, len(resp))
	for i, v := range resp {
		versions[i] = v
	}

	return &ResourceCheckResult{Versions: versions}, nil
}

// ResourceFetchInput is the input for a Fetch operation from JS.
type ResourceFetchInput struct {
	Type    string            `json:"type"`
	Source  map[string]any    `json:"source"`
	Version map[string]string `json:"version"`
	Params  map[string]any    `json:"params,omitempty"`
	DestDir string            `json:"destDir"`
}

// ResourceFetchResult is the result of a Fetch operation.
type ResourceFetchResult struct {
	Version  map[string]string `json:"version"`
	Metadata []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"metadata"`
}

// Fetch retrieves a specific version of a resource (equivalent to 'in' or 'get').
func (r *ResourceRunner) Fetch(input ResourceFetchInput) (*ResourceFetchResult, error) {
	logger := r.logger.With("type", input.Type, "operation", "resource.fetch", "destDir", input.DestDir)
	logger.Debug("resource.fetch")

	err := support.ResolveSecretsInMap(r.ctx, r.secretsManager, r.pipelineID, input.Source, nil)
	if err != nil {
		return nil, fmt.Errorf("could not resolve secrets in source: %w", err)
	}

	err = support.ResolveSecretsInMap(r.ctx, r.secretsManager, r.pipelineID, input.Params, nil)
	if err != nil {
		return nil, fmt.Errorf("could not resolve secrets in params: %w", err)
	}

	res, err := resources.Get(input.Type)
	if err != nil {
		return nil, fmt.Errorf("resource type not found: %w", err)
	}

	req := resources.InRequest{
		Source:  input.Source,
		Version: input.Version,
		Params:  input.Params,
	}

	resp, err := res.In(r.ctx, &resources.DirVolumeContext{Dir: input.DestDir}, req)
	if err != nil {
		logger.Error("resource.fetch.failed", "err", err)

		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	result := &ResourceFetchResult{
		Version: resp.Version,
	}

	for _, m := range resp.Metadata {
		result.Metadata = append(result.Metadata, struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: m.Name, Value: m.Value})
	}

	return result, nil
}

// ResourcePushInput is the input for a Push operation from JS.
type ResourcePushInput struct {
	Type   string         `json:"type"`
	Source map[string]any `json:"source"`
	Params map[string]any `json:"params,omitempty"`
	SrcDir string         `json:"srcDir"`
}

// ResourcePushResult is the result of a Push operation.
type ResourcePushResult struct {
	Version  map[string]string `json:"version"`
	Metadata []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"metadata"`
}

// Push publishes a new version of a resource (equivalent to 'out' or 'put').
func (r *ResourceRunner) Push(input ResourcePushInput) (*ResourcePushResult, error) {
	logger := r.logger.With("type", input.Type, "operation", "resource.push", "srcDir", input.SrcDir)
	logger.Debug("resource.push")

	err := support.ResolveSecretsInMap(r.ctx, r.secretsManager, r.pipelineID, input.Source, nil)
	if err != nil {
		return nil, fmt.Errorf("could not resolve secrets in source: %w", err)
	}

	err = support.ResolveSecretsInMap(r.ctx, r.secretsManager, r.pipelineID, input.Params, nil)
	if err != nil {
		return nil, fmt.Errorf("could not resolve secrets in params: %w", err)
	}

	res, err := resources.Get(input.Type)
	if err != nil {
		return nil, fmt.Errorf("resource type not found: %w", err)
	}

	req := resources.OutRequest{
		Source: input.Source,
		Params: input.Params,
	}

	resp, err := res.Out(r.ctx, &resources.DirVolumeContext{Dir: input.SrcDir}, req)
	if err != nil {
		logger.Error("resource.push.failed", "err", err)

		return nil, fmt.Errorf("push failed: %w", err)
	}

	result := &ResourcePushResult{
		Version: resp.Version,
	}

	for _, m := range resp.Metadata {
		result.Metadata = append(result.Metadata, struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: m.Name, Value: m.Value})
	}

	return result, nil
}

// IsNative returns true if the given resource type is a native resource.
func (r *ResourceRunner) IsNative(resourceType string) bool {
	return resources.IsNative(resourceType)
}

// ListNativeResources returns a list of all registered native resource types.
func (r *ResourceRunner) ListNativeResources() []string {
	return resources.List()
}

// NativeResourceInfo holds information about resource execution for JSON serialization.
type NativeResourceInfo struct {
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}
