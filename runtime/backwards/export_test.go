package backwards

import (
	config "github.com/jtarchie/pocketci/backwards"
)

// ExportFindDependentJobs exposes findDependentJobs for benchmarking.
// It builds the index on first call — callers should reuse the same Runner across
// iterations to benchmark the O(1) lookup path rather than index construction.
func ExportFindDependentJobs(cfg *config.Config, jobName string) []*config.Job {
	r := &Runner{config: cfg}
	r.buildDependentsIndex()
	return r.findDependentJobs(jobName)
}

// ExportBuildDependentsIndex exposes buildDependentsIndex for benchmarking index construction.
func ExportBuildDependentsIndex(cfg *config.Config) *Runner {
	r := &Runner{config: cfg}
	r.buildDependentsIndex()
	return r
}

// ExportFindDependentJobsCached exposes findDependentJobs on a pre-indexed Runner.
func ExportFindDependentJobsCached(r *Runner, jobName string) []*config.Job {
	return r.findDependentJobs(jobName)
}

// Combination is the exported alias for the internal combination type for benchmarking.
type Combination = combination

// ExportGenerateCombinations exposes generateCombinations for benchmarking.
func ExportGenerateCombinations(vars []config.AcrossVar) []Combination {
	return generateCombinations(vars)
}

// ExportMergeJobParams exposes mergeJobParams for benchmarking.
func ExportMergeJobParams(jobParams, stepEnv map[string]string) map[string]string {
	return mergeJobParams(jobParams, stepEnv)
}

// ExportCloneEnv exposes cloneEnv for benchmarking.
func ExportCloneEnv(env map[string]string) map[string]string {
	return cloneEnv(env)
}

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
