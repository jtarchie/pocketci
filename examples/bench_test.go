package main_test

import (
	"path/filepath"
	"testing"

	_ "github.com/jtarchie/pocketci/orchestra/docker"
	"github.com/jtarchie/pocketci/testhelpers"
)

// BenchmarkPipeline_HelloWorld measures full pipeline execution overhead
// using the hello-world.ts example with docker driver.
func BenchmarkPipeline_HelloWorld(b *testing.B) {
	examplePath, err := filepath.Abs("both/hello-world.ts")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		runner := testhelpers.Runner{
			Pipeline:          examplePath,
			Driver:            "docker",
			StorageSQLitePath: ":memory:",
		}
		if err := runner.Run(nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPipeline_Promises measures parallel task execution with docker driver.
func BenchmarkPipeline_Promises(b *testing.B) {
	examplePath, err := filepath.Abs("both/promises.ts")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		runner := testhelpers.Runner{
			Pipeline:          examplePath,
			Driver:            "docker",
			StorageSQLitePath: ":memory:",
		}
		if err := runner.Run(nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPipeline_Minimal measures minimal container startup overhead with docker.
func BenchmarkPipeline_Minimal(b *testing.B) {
	examplePath, err := filepath.Abs("both/bench-minimal.ts")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		runner := testhelpers.Runner{
			Pipeline:          examplePath,
			Driver:            "docker",
			StorageSQLitePath: ":memory:",
		}
		if err := runner.Run(nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPipeline_Volumes measures volume operations with docker driver.
func BenchmarkPipeline_Volumes(b *testing.B) {
	examplePath, err := filepath.Abs("both/volumes.ts")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		runner := testhelpers.Runner{
			Pipeline:          examplePath,
			Driver:            "docker",
			StorageSQLitePath: ":memory:",
		}
		if err := runner.Run(nil); err != nil {
			b.Fatal(err)
		}
	}
}
