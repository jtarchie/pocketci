package backwards_test

import (
	"fmt"
	"testing"

	config "github.com/jtarchie/pocketci/backwards"
	backwards "github.com/jtarchie/pocketci/runtime/backwards"
)

func buildLinearPipelineConfig(numJobs int) *config.Config {
	jobs := make([]config.Job, numJobs)
	jobs[0] = config.Job{
		Name: "job-0",
		Plan: config.Steps{{
			Task: "task-0",
			TaskConfig: &config.TaskConfig{
				Platform: "linux",
				Run:      &config.TaskConfigRun{Path: "true"},
			},
		}},
	}

	for i := 1; i < numJobs; i++ {
		name := fmt.Sprintf("job-%d", i)
		prev := fmt.Sprintf("job-%d", i-1)
		jobs[i] = config.Job{
			Name: name,
			Plan: config.Steps{
				{
					Get: "dummy",
					GetConfig: config.GetConfig{
						Passed: []string{prev},
					},
				},
				{
					Task: fmt.Sprintf("task-%s", name),
					TaskConfig: &config.TaskConfig{
						Platform: "linux",
						Run:      &config.TaskConfigRun{Path: "true"},
					},
				},
			},
		}
	}

	return &config.Config{Jobs: jobs}
}

func BenchmarkFindDependentJobs_10(b *testing.B) {
	benchmarkFindDependentJobs(b, 10)
}

func BenchmarkFindDependentJobs_100(b *testing.B) {
	benchmarkFindDependentJobs(b, 100)
}

func BenchmarkFindDependentJobs_500(b *testing.B) {
	benchmarkFindDependentJobs(b, 500)
}

func benchmarkFindDependentJobs(b *testing.B, numJobs int) {
	b.Helper()

	cfg := buildLinearPipelineConfig(numJobs)
	// Pre-build index so we benchmark the O(1) lookup, not index construction.
	r := backwards.ExportBuildDependentsIndex(cfg)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		for i := range numJobs {
			_ = backwards.ExportFindDependentJobsCached(r, fmt.Sprintf("job-%d", i))
		}
	}
}

func BenchmarkBuildDependentsIndex_100(b *testing.B) {
	cfg := buildLinearPipelineConfig(100)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = backwards.ExportBuildDependentsIndex(cfg)
	}
}
