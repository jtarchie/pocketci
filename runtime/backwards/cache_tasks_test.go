package backwards

import (
	"strings"
	"testing"

	"github.com/jtarchie/pocketci/runtime/cacheconfig"
)

// TestCacheS3KeyShape pins the S3 key format produced by cacheS3Key. The
// shape is load-bearing: it's what allows cache_op to share a bucket across
// pipelines/jobs without collisions and to find the previous run's archive
// without a per-run identifier. Two pipelines or two jobs must produce
// distinct keys; the same (pipeline, job, volume) tuple must produce the
// same key across runs.
func TestCacheS3KeyShape(t *testing.T) {
	t.Parallel()

	cfg := &cacheconfig.S3{Prefix: "test-prefix"}

	keyA := cacheS3Key(cfg, "pipeline-a", "build", "cache-deps")
	keyB := cacheS3Key(cfg, "pipeline-b", "build", "cache-deps")

	if keyA == keyB {
		t.Fatalf("different pipelines should produce different keys: both got %q", keyA)
	}

	if !strings.HasPrefix(keyA, "test-prefix/") {
		t.Fatalf("expected key %q to start with prefix", keyA)
	}

	if !strings.HasSuffix(keyA, ".tar.zst") {
		t.Fatalf("expected key %q to end with .tar.zst", keyA)
	}

	// Stability across runs: cacheS3Key is a pure function, so identical
	// inputs always yield identical outputs. This guards against accidentally
	// folding run-scoped data (e.g. runID) into the key, which would make
	// every run a cold cache.
	again := cacheS3Key(cfg, "pipeline-a", "build", "cache-deps")
	if keyA != again {
		t.Fatalf("same inputs should produce same key, got %q vs %q", keyA, again)
	}

	// Different jobs in the same pipeline must isolate.
	keyOtherJob := cacheS3Key(cfg, "pipeline-a", "test", "cache-deps")
	if keyA == keyOtherJob {
		t.Fatalf("different jobs should produce different keys: both got %q", keyA)
	}

	// Task scope is encoded by the caller in volumeName (see
	// task_handler.go resolveCaches): "cache-<task>-<path>" for
	// scope=task, "cache-<path>" otherwise. Verify that volume name
	// difference flows through to the key.
	keyTaskA := cacheS3Key(cfg, "pipeline-a", "build", "cache-task-a-deps")
	keyTaskB := cacheS3Key(cfg, "pipeline-a", "build", "cache-task-b-deps")
	if keyTaskA == keyTaskB {
		t.Fatalf("different task-scoped volume names should produce different keys: both got %q", keyTaskA)
	}
}

// TestCacheS3KeyNoPrefix verifies that an empty prefix is handled cleanly
// (no leading slash, no empty path segment).
func TestCacheS3KeyNoPrefix(t *testing.T) {
	t.Parallel()

	cfg := &cacheconfig.S3{}
	key := cacheS3Key(cfg, "p", "j", "v")

	if strings.HasPrefix(key, "/") {
		t.Fatalf("key %q should not start with /", key)
	}

	want := "p/j/v.tar.zst"
	if key != want {
		t.Fatalf("expected %q, got %q", want, key)
	}
}
