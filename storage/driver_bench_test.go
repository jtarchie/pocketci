package storage_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/jtarchie/pocketci/storage"
	storagesqlite "github.com/jtarchie/pocketci/storage/sqlite"
)

func setupBenchDB(b *testing.B) storage.Driver {
	b.Helper()

	// Use a temp file instead of :memory: because the sqlite driver
	// opens separate reader/writer connections which don't share memory DBs
	tmpFile, err := os.CreateTemp("", "bench-*.db")
	if err != nil {
		b.Fatal(err)
	}
	_ = tmpFile.Close()

	driver, err := storagesqlite.NewSqlite(storagesqlite.Config{
		Path: tmpFile.Name(),
	}, "bench", slog.Default())
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		_ = driver.Close()
		_ = os.Remove(tmpFile.Name())
	})

	return driver
}

func BenchmarkStorage_Set(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()
	payload := map[string]any{"status": "success", "output": "hello world", "exit_code": 0}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		key := fmt.Sprintf("/task/%d", b.N)
		if err := driver.Set(ctx, key, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorage_Get(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()
	payload := map[string]any{"status": "success", "output": "hello world", "exit_code": 0}

	// Pre-populate data
	for i := range 1000 {
		key := fmt.Sprintf("/task/%d", i)
		if err := driver.Set(ctx, key, payload); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := range b.N {
		key := fmt.Sprintf("/task/%d", i%1000)
		if _, err := driver.Get(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorage_GetAll(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()
	payload := map[string]any{"status": "success", "output": "hello world", "exit_code": 0}

	// Pre-populate data using the Set method which populates the tasks table
	for i := range 100 {
		key := fmt.Sprintf("/bench/tasks/%d", i)
		if err := driver.Set(ctx, key, payload); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := driver.GetAll(ctx, "/bench/tasks/", []string{"status"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorage_SavePipeline(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()
	content := `const pipeline = async () => { await runtime.run({ name: "test", image: "busybox", command: { path: "true" } }); }; export { pipeline };`

	b.ReportAllocs()
	b.ResetTimer()

	for i := range b.N {
		name := fmt.Sprintf("pipeline-%d", i)
		if _, err := driver.SavePipeline(ctx, name, content, "docker", ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorage_ListPipelines(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()
	content := `const pipeline = async () => {}; export { pipeline };`

	// Pre-populate pipelines
	for i := range 50 {
		name := fmt.Sprintf("pipeline-%d", i)
		if _, err := driver.SavePipeline(ctx, name, content, "docker", ""); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := driver.SearchPipelines(ctx, "", 1, 100); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorage_SaveAndGetRun(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()
	content := `const pipeline = async () => {}; export { pipeline };`

	// Create a pipeline first
	pipeline, err := driver.SavePipeline(ctx, "bench-pipeline", content, "docker", "")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		run, err := driver.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		if err != nil {
			b.Fatal(err)
		}

		if _, err := driver.GetRun(ctx, run.ID); err != nil {
			b.Fatal(err)
		}

		if err := driver.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorage_UpdateRunStatus_Allowed(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()

	pipeline, err := driver.SavePipeline(ctx, "bench-allowed", "content", "docker", "")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		run, err := driver.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
		if err != nil {
			b.Fatal(err)
		}

		// queued -> running (allowed)
		if err := driver.UpdateRunStatus(ctx, run.ID, storage.RunStatusRunning, ""); err != nil {
			b.Fatal(err)
		}

		// running -> success (allowed)
		if err := driver.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorage_UpdateRunStatus_Blocked(b *testing.B) {
	driver := setupBenchDB(b)
	ctx := context.Background()

	pipeline, err := driver.SavePipeline(ctx, "bench-blocked", "content", "docker", "")
	if err != nil {
		b.Fatal(err)
	}

	// Pre-create a run in terminal state
	run, err := driver.SaveRun(ctx, pipeline.ID, storage.TriggerTypeManual, "", storage.TriggerInput{})
	if err != nil {
		b.Fatal(err)
	}

	if err := driver.UpdateRunStatus(ctx, run.ID, storage.RunStatusFailed, "stopped"); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		// failed -> success: blocked by state machine (silent no-op)
		if err := driver.UpdateRunStatus(ctx, run.ID, storage.RunStatusSuccess, ""); err != nil {
			b.Fatal(err)
		}
	}
}
