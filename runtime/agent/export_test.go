package agent

import (
	"context"

	adktool "google.golang.org/adk/tool"
)

// BuildMemoryToolsForTest exposes buildMemoryTools for black-box tests in the
// agent_test package. Not part of the public API.
func BuildMemoryToolsForTest(ctx context.Context, config AgentConfig) ([]adktool.Tool, error) {
	return buildMemoryTools(ctx, config)
}
