package backwards_test

import (
	"testing"

	backwards "github.com/jtarchie/pocketci/runtime/backwards"
)

func BenchmarkMergeJobParams_NoJobParams(b *testing.B) {
	stepEnv := map[string]string{"FOO": "bar", "BAZ": "qux", "PATH": "/usr/bin", "HOME": "/root"}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = backwards.ExportMergeJobParams(nil, stepEnv)
	}
}

func BenchmarkMergeJobParams_NoStepEnv(b *testing.B) {
	jobParams := map[string]string{"BRANCH": "main", "SHA": "abc123def456"}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = backwards.ExportMergeJobParams(jobParams, nil)
	}
}

func BenchmarkMergeJobParams_WithBoth(b *testing.B) {
	jobParams := map[string]string{"BRANCH": "main", "SHA": "abc123def456"}
	stepEnv := map[string]string{"FOO": "bar", "BAZ": "qux", "PATH": "/usr/bin", "HOME": "/root"}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = backwards.ExportMergeJobParams(jobParams, stepEnv)
	}
}

func BenchmarkCloneEnv(b *testing.B) {
	env := map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5", "F": "6", "G": "7"}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = backwards.ExportCloneEnv(env)
	}
}
