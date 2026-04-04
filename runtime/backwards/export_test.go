package backwards

import (
	config "github.com/jtarchie/pocketci/backwards"
)

// TestStep wraps config.Step fields for test construction.
type TestStep struct {
	Image             string
	ImageResourceRepo string
	Prompt            string
	Model             string
}

// ResolveAgentImage exports resolveAgentImage for testing.
func ResolveAgentImage(ts *TestStep) string {
	step := &config.Step{}

	if ts.Image != "" || ts.ImageResourceRepo != "" {
		step.TaskConfig = &config.TaskConfig{
			Image: ts.Image,
		}

		if ts.ImageResourceRepo != "" {
			step.TaskConfig.ImageResource = config.ImageResource{
				Source: map[string]any{"repository": ts.ImageResourceRepo},
			}
		}
	}

	return resolveAgentImage(step)
}

// MergeResult holds the merged result for test assertions.
type MergeResult struct {
	Prompt string
	Model  string
}

// MergeAgentFromContents exports mergeAgentFromContents for testing.
func MergeAgentFromContents(contents []byte, ts *TestStep) *MergeResult {
	step := &config.Step{
		Prompt: ts.Prompt,
		Model:  ts.Model,
	}

	merged := mergeAgentFromContents(contents, step)

	return &MergeResult{
		Prompt: merged.Prompt,
		Model:  merged.Model,
	}
}
